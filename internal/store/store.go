// Package store is the SQLite persistence layer for stash-janitor.
//
// All scan results, scoring decisions, and user overrides live in a single
// SQLite file (default ./stash-janitor.sqlite). Using SQLite means re-runs are fast,
// reports work offline, and a crash mid-apply doesn't lose your decisions.
//
// We use modernc.org/sqlite — a pure-Go SQLite driver — so the binary stays
// statically linkable on any platform without cgo.
//
// The schema lives in schema.sql, embedded into the binary, and is applied
// idempotently on Open. Phase 1 has only one schema version; future schema
// changes should add ALTER TABLE / new tables in numbered migration steps.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// currentSchemaVersion is the highest schema version this binary knows how
// to apply. Bump it when you add a migration step in applySchema.
//
// History:
//
//	v1 — initial Phase 1 schema
//	v2 — added scene_group_scenes.filename_quality (filename info loss
//	     safety net for workflow A)
//	v3 — added fingerprint_submissions table (--submit-fingerprints)
//	v4 — added orphan_lookups table (Phase 3 workflow C: stash-box phash
//	     lookup for scenes with no stash_id)
//	v5 — added organize_plans table (workflow D: file organization)
const currentSchemaVersion = 5

// Status values used by both scene_groups.status and file_groups.status.
const (
	StatusPending     = "pending"
	StatusDecided     = "decided"
	StatusNeedsReview = "needs_review"
	StatusApplied     = "applied"
	StatusDismissed   = "dismissed"
)

// Role values used by both scene_group_scenes.role and file_group_files.role.
const (
	RoleKeeper    = "keeper"
	RoleLoser     = "loser"
	RoleUndecided = "undecided"
)

// Workflow tags used in scan_runs.workflow and user_decisions.workflow.
const (
	WorkflowScenes   = "scenes"
	WorkflowFiles    = "files"
	WorkflowOrphans  = "orphans"
	WorkflowOrganize = "organize"
)

// Status values for orphan_lookups specifically. Re-uses the generic
// StatusApplied / StatusDismissed / StatusNeedsReview but adds two
// orphan-specific ones.
const (
	StatusMatched = "matched"
	StatusNoMatch = "no_match"
)

// Store wraps a database/sql connection to a SQLite file. Safe for
// concurrent use; database/sql handles connection pooling internally.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite store at the given path and applies the
// embedded schema. Idempotent — calling Open twice on the same file is fine.
func Open(path string) (*Store, error) {
	// Important DSN params:
	//   _pragma=busy_timeout(...) — wait for locks instead of failing fast
	//   _pragma=journal_mode(WAL) — concurrent reader+writer
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite at %s: %w", path, err)
	}
	// modernc/sqlite is happy with one writer; cap to keep behavior obvious.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.applySchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the raw *sql.DB. Exposed for tests and the rare case where a
// caller needs to run an ad-hoc query; production code should use the
// typed methods on Store.
func (s *Store) DB() *sql.DB {
	return s.db
}

// applySchema runs the embedded schema.sql idempotently and applies any
// pending version migrations.
//
// The contract:
//
//   - schema.sql always reflects the LATEST schema. Fresh databases get
//     every column at creation time and skip migrations.
//   - Old databases run only the migration steps strictly above their
//     recorded version, in order, then record the new version.
//   - A fresh database is detected by an empty schema_version table; we
//     immediately stamp it at currentSchemaVersion and return.
func (s *Store) applySchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema_version: %w", err)
	}

	if current == 0 {
		// Fresh database — schema.sql created the latest tables already.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(?)`, currentSchemaVersion); err != nil {
			return fmt.Errorf("recording schema_version=%d: %w", currentSchemaVersion, err)
		}
		return nil
	}

	for v := current + 1; v <= currentSchemaVersion; v++ {
		if err := s.applyMigration(ctx, v); err != nil {
			return fmt.Errorf("schema migration v%d: %w", v, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(?)`, v); err != nil {
			return fmt.Errorf("recording schema_version=%d: %w", v, err)
		}
	}
	return nil
}

// applyMigration runs the SQL needed to bring the database from version
// `version-1` to `version`. Each step is idempotent in spirit (if it fails
// halfway, the next run sees the same `current` and retries the same step).
func (s *Store) applyMigration(ctx context.Context, version int) error {
	switch version {
	case 2:
		// Add filename_quality to scene_group_scenes for the workflow A
		// "filename info loss" safety net. Default 0 so existing rows are
		// treated as "no filename match".
		_, err := s.db.ExecContext(ctx, `ALTER TABLE scene_group_scenes ADD COLUMN filename_quality INTEGER NOT NULL DEFAULT 0`)
		return err
	case 3:
		// Add fingerprint_submissions table for the --submit-fingerprints
		// flag. Tracks (scene_id, stash-box endpoint) pairs so re-runs
		// don't double-submit.
		_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS fingerprint_submissions (
			scene_id     TEXT NOT NULL,
			endpoint     TEXT NOT NULL,
			submitted_at TEXT NOT NULL,
			PRIMARY KEY (scene_id, endpoint)
		)`)
		return err
	case 4:
		// Add orphan_lookups table for the workflow C orphan stash-box
		// lookup. The single CREATE TABLE statement creates the latest
		// shape; idx_ creations follow.
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS orphan_lookups (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				scan_run_id      INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
				scene_id         TEXT NOT NULL,
				endpoint         TEXT NOT NULL,
				status           TEXT NOT NULL,
				decision_reason  TEXT,
				decided_at       TEXT,
				applied_at       TEXT,
				primary_path     TEXT,
				basename         TEXT,
				duration         REAL,
				width            INTEGER,
				height           INTEGER,
				match_remote_id  TEXT,
				match_title      TEXT,
				match_studio     TEXT,
				match_date       TEXT,
				match_performers TEXT,
				match_count      INTEGER NOT NULL DEFAULT 0,
				UNIQUE(scan_run_id, scene_id, endpoint)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_orphan_lookups_status ON orphan_lookups(status)`,
			`CREATE INDEX IF NOT EXISTS idx_orphan_lookups_scene  ON orphan_lookups(scene_id, endpoint)`,
		}
		for _, stmt := range stmts {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	case 5:
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS organize_plans (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				scan_run_id  INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
				scene_id     TEXT NOT NULL,
				file_id      TEXT NOT NULL,
				current_path TEXT NOT NULL,
				target_path  TEXT NOT NULL,
				status       TEXT NOT NULL,
				reason       TEXT,
				applied_at   TEXT,
				UNIQUE(scan_run_id, file_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_organize_plans_status ON organize_plans(status)`,
		}
		for _, stmt := range stmts {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown migration version %d (this is a bug)", version)
	}
}

// nowRFC3339 returns the current UTC time in the canonical text format we
// store in TEXT columns. Centralized so tests and production agree.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// nullableString turns a Go string into a sql.NullString that's NULL when
// the string is empty. Used to keep "missing" and "empty" distinguishable
// in TEXT columns where empty would be ambiguous.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullableTime turns a *time.Time into a sql.NullString in our canonical
// format. nil → NULL.
func nullableTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}

// scanTime decodes one of our TEXT timestamp columns into a *time.Time.
// Returns nil if the column was NULL or empty.
func scanTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		// Best effort — fall back to RFC3339 without nanos.
		t, err = time.Parse(time.RFC3339, ns.String)
		if err != nil {
			return nil
		}
	}
	return &t
}

// ErrNotFound is returned by Get* methods when no row matches.
var ErrNotFound = errors.New("store: not found")

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
const currentSchemaVersion = 1

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
	WorkflowScenes = "scenes"
	WorkflowFiles  = "files"
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

// applySchema runs the embedded schema.sql idempotently and records the
// schema version. Future migrations should switch on the recorded version
// and apply only the missing steps.
func (s *Store) applySchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}

	var current int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current)
	if err != nil {
		return fmt.Errorf("reading schema_version: %w", err)
	}

	if current >= currentSchemaVersion {
		return nil
	}

	// Phase 1 has only schema version 1, established by schema.sql above.
	// Future versions: insert handlers here, each gated on `current < N`.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(?)`, currentSchemaVersion); err != nil {
		return fmt.Errorf("recording schema_version=%d: %w", currentSchemaVersion, err)
	}
	return nil
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

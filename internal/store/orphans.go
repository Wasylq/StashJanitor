package store

import (
	"context"
	"database/sql"
	"fmt"
)

// UpsertOrphanLookup inserts or updates a single orphan lookup row.
// (scan_run_id, scene_id, endpoint) is the unique constraint — re-running
// scan within one run is idempotent.
func (s *Store) UpsertOrphanLookup(ctx context.Context, o *OrphanLookup) error {
	var existingID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM orphan_lookups WHERE scan_run_id = ? AND scene_id = ? AND endpoint = ?`,
		o.ScanRunID, o.SceneID, o.Endpoint,
	).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		res, ierr := s.db.ExecContext(ctx,
			`INSERT INTO orphan_lookups
			 (scan_run_id, scene_id, endpoint, status, decision_reason,
			  decided_at, applied_at, primary_path, basename, duration,
			  width, height, match_remote_id, match_title, match_studio,
			  match_date, match_performers, match_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.ScanRunID, o.SceneID, o.Endpoint, o.Status,
			nullableString(o.DecisionReason),
			nullableTime(o.DecidedAt), nullableTime(o.AppliedAt),
			nullableString(o.PrimaryPath), nullableString(o.Basename), o.Duration,
			o.Width, o.Height,
			nullableString(o.MatchRemoteID), nullableString(o.MatchTitle),
			nullableString(o.MatchStudio), nullableString(o.MatchDate),
			nullableString(o.MatchPerformers), o.MatchCount,
		)
		if ierr != nil {
			return fmt.Errorf("inserting orphan_lookups: %w", ierr)
		}
		o.ID, err = res.LastInsertId()
		return err
	case err != nil:
		return fmt.Errorf("looking up existing orphan_lookup: %w", err)
	default:
		o.ID = existingID
		_, uerr := s.db.ExecContext(ctx,
			`UPDATE orphan_lookups SET
			   status = ?, decision_reason = ?, decided_at = ?, applied_at = ?,
			   primary_path = ?, basename = ?, duration = ?, width = ?, height = ?,
			   match_remote_id = ?, match_title = ?, match_studio = ?,
			   match_date = ?, match_performers = ?, match_count = ?
			 WHERE id = ?`,
			o.Status, nullableString(o.DecisionReason),
			nullableTime(o.DecidedAt), nullableTime(o.AppliedAt),
			nullableString(o.PrimaryPath), nullableString(o.Basename), o.Duration,
			o.Width, o.Height,
			nullableString(o.MatchRemoteID), nullableString(o.MatchTitle),
			nullableString(o.MatchStudio), nullableString(o.MatchDate),
			nullableString(o.MatchPerformers), o.MatchCount,
			o.ID,
		)
		return uerr
	}
}

// HasOrphanLookup reports whether (scene, endpoint) already has a row in
// orphan_lookups across any scan run. Used by `stash-janitor orphans scan` to skip
// previously-looked-up orphans on re-runs (unless --rescan is set).
func (s *Store) HasOrphanLookup(ctx context.Context, sceneID, endpoint string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM orphan_lookups WHERE scene_id = ? AND endpoint = ?`,
		sceneID, endpoint,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListOrphanLookups returns rows matching the given statuses (nil = all),
// ordered by scene_id.
func (s *Store) ListOrphanLookups(ctx context.Context, statuses []string) ([]*OrphanLookup, error) {
	query := `SELECT id, scan_run_id, scene_id, endpoint, status,
	                  COALESCE(decision_reason,''), decided_at, applied_at,
	                  COALESCE(primary_path,''), COALESCE(basename,''),
	                  COALESCE(duration,0), COALESCE(width,0), COALESCE(height,0),
	                  COALESCE(match_remote_id,''), COALESCE(match_title,''),
	                  COALESCE(match_studio,''), COALESCE(match_date,''),
	                  COALESCE(match_performers,''), match_count
	          FROM orphan_lookups`
	args := []any{}
	if len(statuses) > 0 {
		query += ` WHERE status IN (`
		for i, st := range statuses {
			if i > 0 {
				query += `,`
			}
			query += `?`
			args = append(args, st)
		}
		query += `)`
	}
	query += ` ORDER BY scene_id ASC, endpoint ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListOrphanLookups: %w", err)
	}
	defer rows.Close()

	var out []*OrphanLookup
	for rows.Next() {
		var o OrphanLookup
		var decidedAt, appliedAt sql.NullString
		if err := rows.Scan(
			&o.ID, &o.ScanRunID, &o.SceneID, &o.Endpoint, &o.Status,
			&o.DecisionReason, &decidedAt, &appliedAt,
			&o.PrimaryPath, &o.Basename,
			&o.Duration, &o.Width, &o.Height,
			&o.MatchRemoteID, &o.MatchTitle, &o.MatchStudio,
			&o.MatchDate, &o.MatchPerformers, &o.MatchCount,
		); err != nil {
			return nil, err
		}
		o.DecidedAt = scanTime(decidedAt)
		o.AppliedAt = scanTime(appliedAt)
		out = append(out, &o)
	}
	return out, rows.Err()
}

// MarkOrphanLookupApplied flips the row's status to "applied" and records
// applied_at. Used by `stash-janitor orphans apply --commit` after writing the
// stash_id back to Stash.
func (s *Store) MarkOrphanLookupApplied(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE orphan_lookups SET applied_at = ?, status = ? WHERE id = ?`,
		nowRFC3339(), StatusApplied, id,
	)
	return err
}

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// UpsertSceneGroup inserts or updates a workflow A duplicate group along
// with all its member scenes, in a single transaction. The (scan_run_id,
// signature) UNIQUE constraint is the conflict target — re-running scan
// for the same group within one run is a no-op.
//
// Caller is responsible for setting g.ScanRunID and g.Signature; the rest
// of the group fields are written as-is. Member scenes are fully replaced
// (delete + insert) so a re-upsert reflects the latest snapshot.
func (s *Store) UpsertSceneGroup(ctx context.Context, g *SceneGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("BeginTx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Look up existing group ID first; SQLite's RETURNING + ON CONFLICT
	// dance is awkward and we want a portable approach.
	var existingID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM scene_groups WHERE scan_run_id = ? AND signature = ?`,
		g.ScanRunID, g.Signature,
	).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		res, ierr := tx.ExecContext(ctx,
			`INSERT INTO scene_groups
			 (scan_run_id, signature, status, decision_reason, decided_at, applied_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			g.ScanRunID, g.Signature, g.Status,
			nullableString(g.DecisionReason),
			nullableTime(g.DecidedAt),
			nullableTime(g.AppliedAt),
		)
		if ierr != nil {
			return fmt.Errorf("inserting scene_groups: %w", ierr)
		}
		g.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("looking up existing scene_group: %w", err)
	default:
		g.ID = existingID
		_, uerr := tx.ExecContext(ctx,
			`UPDATE scene_groups
			 SET status = ?, decision_reason = ?, decided_at = ?, applied_at = ?
			 WHERE id = ?`,
			g.Status, nullableString(g.DecisionReason),
			nullableTime(g.DecidedAt), nullableTime(g.AppliedAt),
			g.ID,
		)
		if uerr != nil {
			return fmt.Errorf("updating scene_groups: %w", uerr)
		}
	}

	// Replace member scenes wholesale. The CASCADE handles delete on its
	// own, but we delete here too in case we ever drop the foreign key.
	if _, err := tx.ExecContext(ctx, `DELETE FROM scene_group_scenes WHERE group_id = ?`, g.ID); err != nil {
		return fmt.Errorf("deleting old scene_group_scenes: %w", err)
	}

	for _, sc := range g.Scenes {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO scene_group_scenes
			 (group_id, scene_id, role,
			  width, height, bitrate, framerate, codec, file_size, duration,
			  organized, has_stash_id, tag_count, performer_count, primary_path,
			  filename_quality)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.ID, sc.SceneID, sc.Role,
			sc.Width, sc.Height, sc.Bitrate, sc.Framerate, sc.Codec, sc.FileSize, sc.Duration,
			boolToInt(sc.Organized), boolToInt(sc.HasStashID),
			sc.TagCount, sc.PerformerCount, sc.PrimaryPath,
			sc.FilenameQuality,
		)
		if err != nil {
			return fmt.Errorf("inserting scene_group_scenes(%s): %w", sc.SceneID, err)
		}
	}

	return tx.Commit()
}

// GetSceneGroupBySignature returns the most recent scene_groups row matching
// the given signature, including its member scenes. Used by the scan loop
// to detect previously-seen groups across runs.
func (s *Store) GetSceneGroupBySignature(ctx context.Context, signature string) (*SceneGroup, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, scan_run_id, signature, status,
		        COALESCE(decision_reason, ''), decided_at, applied_at
		 FROM scene_groups WHERE signature = ?
		 ORDER BY id DESC LIMIT 1`,
		signature,
	)
	g, err := scanSceneGroup(row)
	if err != nil {
		return nil, err
	}
	if err := s.loadSceneGroupScenes(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// ListSceneGroups returns all scene groups whose status matches one of the
// provided values, ordered by id. Pass nil/empty to list every status.
func (s *Store) ListSceneGroups(ctx context.Context, statuses []string) ([]*SceneGroup, error) {
	query := `SELECT id, scan_run_id, signature, status,
	                  COALESCE(decision_reason, ''), decided_at, applied_at
	          FROM scene_groups`
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
	query += ` ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListSceneGroups query: %w", err)
	}
	defer rows.Close()

	var out []*SceneGroup
	for rows.Next() {
		g, err := scanSceneGroupRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Loading member scenes per group keeps the API simple at the cost of
	// N+1 queries. At Phase 1 scale (hundreds of groups) this is fine. If
	// it becomes a bottleneck, switch to a single JOIN with grouping.
	for _, g := range out {
		if err := s.loadSceneGroupScenes(ctx, g); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MarkSceneGroupApplied sets applied_at on a group, used by the apply
// commands to make repeated apply runs idempotent.
func (s *Store) MarkSceneGroupApplied(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scene_groups SET applied_at = ?, status = ? WHERE id = ?`,
		nowRFC3339(), StatusApplied, id,
	)
	return err
}

// ----- internal scan helpers -----

func (s *Store) loadSceneGroupScenes(ctx context.Context, g *SceneGroup) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scene_id, role,
		        COALESCE(width,0), COALESCE(height,0), COALESCE(bitrate,0),
		        COALESCE(framerate,0), COALESCE(codec,''), COALESCE(file_size,0),
		        COALESCE(duration,0),
		        organized, has_stash_id, tag_count, performer_count,
		        COALESCE(primary_path,''),
		        filename_quality
		 FROM scene_group_scenes WHERE group_id = ?
		 ORDER BY scene_id`,
		g.ID,
	)
	if err != nil {
		return fmt.Errorf("loading scene_group_scenes: %w", err)
	}
	defer rows.Close()
	g.Scenes = nil
	for rows.Next() {
		var sc SceneGroupScene
		var organized, hasStashID int
		if err := rows.Scan(
			&sc.SceneID, &sc.Role,
			&sc.Width, &sc.Height, &sc.Bitrate,
			&sc.Framerate, &sc.Codec, &sc.FileSize, &sc.Duration,
			&organized, &hasStashID, &sc.TagCount, &sc.PerformerCount,
			&sc.PrimaryPath,
			&sc.FilenameQuality,
		); err != nil {
			return err
		}
		sc.Organized = organized != 0
		sc.HasStashID = hasStashID != 0
		g.Scenes = append(g.Scenes, sc)
	}
	return rows.Err()
}

func scanSceneGroup(row *sql.Row) (*SceneGroup, error) {
	var g SceneGroup
	var decidedAt, appliedAt sql.NullString
	if err := row.Scan(&g.ID, &g.ScanRunID, &g.Signature, &g.Status,
		&g.DecisionReason, &decidedAt, &appliedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.DecidedAt = scanTime(decidedAt)
	g.AppliedAt = scanTime(appliedAt)
	return &g, nil
}

func scanSceneGroupRow(rows *sql.Rows) (*SceneGroup, error) {
	var g SceneGroup
	var decidedAt, appliedAt sql.NullString
	if err := rows.Scan(&g.ID, &g.ScanRunID, &g.Signature, &g.Status,
		&g.DecisionReason, &decidedAt, &appliedAt); err != nil {
		return nil, err
	}
	g.DecidedAt = scanTime(decidedAt)
	g.AppliedAt = scanTime(appliedAt)
	return &g, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// UpsertFileGroup inserts or updates a workflow B multi-file scene with all
// its member files, in a single transaction. (scan_run_id, scene_id) is the
// unique constraint — re-upserting within a run is idempotent.
func (s *Store) UpsertFileGroup(ctx context.Context, fg *FileGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("BeginTx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM file_groups WHERE scan_run_id = ? AND scene_id = ?`,
		fg.ScanRunID, fg.SceneID,
	).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		res, ierr := tx.ExecContext(ctx,
			`INSERT INTO file_groups
			 (scan_run_id, scene_id, status, decision_reason, decided_at, applied_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			fg.ScanRunID, fg.SceneID, fg.Status,
			nullableString(fg.DecisionReason),
			nullableTime(fg.DecidedAt),
			nullableTime(fg.AppliedAt),
		)
		if ierr != nil {
			return fmt.Errorf("inserting file_groups: %w", ierr)
		}
		fg.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("looking up existing file_group: %w", err)
	default:
		fg.ID = existingID
		_, uerr := tx.ExecContext(ctx,
			`UPDATE file_groups
			 SET status = ?, decision_reason = ?, decided_at = ?, applied_at = ?
			 WHERE id = ?`,
			fg.Status, nullableString(fg.DecisionReason),
			nullableTime(fg.DecidedAt), nullableTime(fg.AppliedAt),
			fg.ID,
		)
		if uerr != nil {
			return fmt.Errorf("updating file_groups: %w", uerr)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM file_group_files WHERE file_group_id = ?`, fg.ID); err != nil {
		return fmt.Errorf("deleting old file_group_files: %w", err)
	}

	for _, f := range fg.Files {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO file_group_files
			 (file_group_id, file_id, role, is_primary,
			  basename, path, mod_time, filename_quality,
			  width, height, bitrate, framerate, codec, file_size)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fg.ID, f.FileID, f.Role, boolToInt(f.IsPrimary),
			f.Basename, f.Path, nullableString(f.ModTime), f.FilenameQuality,
			f.Width, f.Height, f.Bitrate, f.Framerate, f.Codec, f.FileSize,
		)
		if err != nil {
			return fmt.Errorf("inserting file_group_files(%s): %w", f.FileID, err)
		}
	}

	return tx.Commit()
}

// ListFileGroups returns all file groups matching the given statuses
// (nil/empty = all), with their member files loaded.
func (s *Store) ListFileGroups(ctx context.Context, statuses []string) ([]*FileGroup, error) {
	query := `SELECT id, scan_run_id, scene_id, status,
	                  COALESCE(decision_reason,''), decided_at, applied_at
	          FROM file_groups`
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
		return nil, fmt.Errorf("ListFileGroups query: %w", err)
	}
	defer rows.Close()

	var out []*FileGroup
	for rows.Next() {
		var fg FileGroup
		var decidedAt, appliedAt sql.NullString
		if err := rows.Scan(&fg.ID, &fg.ScanRunID, &fg.SceneID, &fg.Status,
			&fg.DecisionReason, &decidedAt, &appliedAt); err != nil {
			return nil, err
		}
		fg.DecidedAt = scanTime(decidedAt)
		fg.AppliedAt = scanTime(appliedAt)
		out = append(out, &fg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, fg := range out {
		if err := s.loadFileGroupFiles(ctx, fg); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MarkFileGroupApplied marks a file group as applied. Used by Phase 1.5
// `stash-janitor files apply --commit`.
func (s *Store) MarkFileGroupApplied(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE file_groups SET applied_at = ?, status = ? WHERE id = ?`,
		nowRFC3339(), StatusApplied, id,
	)
	return err
}

func (s *Store) loadFileGroupFiles(ctx context.Context, fg *FileGroup) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_id, role, is_primary, basename, path,
		        COALESCE(mod_time,''), filename_quality,
		        COALESCE(width,0), COALESCE(height,0), COALESCE(bitrate,0),
		        COALESCE(framerate,0), COALESCE(codec,''), COALESCE(file_size,0)
		 FROM file_group_files WHERE file_group_id = ?
		 ORDER BY file_id`,
		fg.ID,
	)
	if err != nil {
		return fmt.Errorf("loading file_group_files: %w", err)
	}
	defer rows.Close()
	fg.Files = nil
	for rows.Next() {
		var f FileGroupFile
		var isPrimary int
		if err := rows.Scan(
			&f.FileID, &f.Role, &isPrimary, &f.Basename, &f.Path,
			&f.ModTime, &f.FilenameQuality,
			&f.Width, &f.Height, &f.Bitrate,
			&f.Framerate, &f.Codec, &f.FileSize,
		); err != nil {
			return err
		}
		f.IsPrimary = isPrimary != 0
		fg.Files = append(fg.Files, f)
	}
	return rows.Err()
}

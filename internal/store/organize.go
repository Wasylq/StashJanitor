package store

import (
	"context"
	"database/sql"
	"fmt"
)

// UpsertOrganizePlan inserts or updates one organize plan row.
func (s *Store) UpsertOrganizePlan(ctx context.Context, p *OrganizePlan) error {
	var existingID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM organize_plans WHERE scan_run_id = ? AND file_id = ?`,
		p.ScanRunID, p.FileID,
	).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		res, ierr := s.db.ExecContext(ctx,
			`INSERT INTO organize_plans
			 (scan_run_id, scene_id, file_id, current_path, target_path, status, reason, applied_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ScanRunID, p.SceneID, p.FileID, p.CurrentPath, p.TargetPath, p.Status,
			nullableString(p.Reason), nullableTime(p.AppliedAt),
		)
		if ierr != nil {
			return fmt.Errorf("inserting organize_plans: %w", ierr)
		}
		p.ID, err = res.LastInsertId()
		return err
	case err != nil:
		return fmt.Errorf("looking up existing organize_plan: %w", err)
	default:
		p.ID = existingID
		_, uerr := s.db.ExecContext(ctx,
			`UPDATE organize_plans SET
			   scene_id = ?, current_path = ?, target_path = ?, status = ?, reason = ?, applied_at = ?
			 WHERE id = ?`,
			p.SceneID, p.CurrentPath, p.TargetPath, p.Status,
			nullableString(p.Reason), nullableTime(p.AppliedAt),
			p.ID,
		)
		return uerr
	}
}

// ListOrganizePlans returns rows matching the given statuses (nil = all).
func (s *Store) ListOrganizePlans(ctx context.Context, statuses []string) ([]*OrganizePlan, error) {
	query := `SELECT id, scan_run_id, scene_id, file_id, current_path, target_path,
	                  status, COALESCE(reason,''), applied_at
	          FROM organize_plans`
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
	query += ` ORDER BY target_path ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListOrganizePlans: %w", err)
	}
	defer rows.Close()

	var out []*OrganizePlan
	for rows.Next() {
		var p OrganizePlan
		var appliedAt sql.NullString
		if err := rows.Scan(
			&p.ID, &p.ScanRunID, &p.SceneID, &p.FileID,
			&p.CurrentPath, &p.TargetPath, &p.Status, &p.Reason, &appliedAt,
		); err != nil {
			return nil, err
		}
		p.AppliedAt = scanTime(appliedAt)
		out = append(out, &p)
	}
	return out, rows.Err()
}

// MarkOrganizePlanApplied marks one row as applied.
func (s *Store) MarkOrganizePlanApplied(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE organize_plans SET applied_at = ?, status = ? WHERE id = ?`,
		nowRFC3339(), StatusApplied, id,
	)
	return err
}

// MarkOrganizePlanFailed records a failure reason on one row.
func (s *Store) MarkOrganizePlanFailed(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE organize_plans SET status = 'failed', reason = ? WHERE id = ?`,
		reason, id,
	)
	return err
}

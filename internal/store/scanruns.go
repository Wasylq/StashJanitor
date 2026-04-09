package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StartScanRun inserts a new scan_runs row and returns its ID. The caller
// then upserts groups attributing them to that ID, and finally calls
// FinishScanRun.
func (s *Store) StartScanRun(ctx context.Context, workflow string, distance *int, durationDiff *float64) (int64, error) {
	if workflow != WorkflowScenes && workflow != WorkflowFiles && workflow != WorkflowOrphans {
		return 0, fmt.Errorf("StartScanRun: invalid workflow %q", workflow)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO scan_runs (workflow, started_at, distance, duration_diff)
		 VALUES (?, ?, ?, ?)`,
		workflow,
		nowRFC3339(),
		nullableInt(distance),
		nullableFloat(durationDiff),
	)
	if err != nil {
		return 0, fmt.Errorf("inserting scan_runs: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("scan_runs LastInsertId: %w", err)
	}
	return id, nil
}

// FinishScanRun marks a scan run as completed and records the group count.
func (s *Store) FinishScanRun(ctx context.Context, id int64, groupCount int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scan_runs SET finished_at = ?, group_count = ? WHERE id = ?`,
		nowRFC3339(), groupCount, id,
	)
	return err
}

// LatestScanRun returns the most recent scan run for the given workflow,
// or ErrNotFound if there is none yet.
func (s *Store) LatestScanRun(ctx context.Context, workflow string) (*ScanRun, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, workflow, started_at, finished_at, distance, duration_diff,
		        COALESCE(group_count, 0), COALESCE(notes, '')
		 FROM scan_runs WHERE workflow = ?
		 ORDER BY id DESC LIMIT 1`,
		workflow,
	)
	return scanScanRun(row)
}

func scanScanRun(row *sql.Row) (*ScanRun, error) {
	var (
		r           ScanRun
		started     string
		finished    sql.NullString
		distance    sql.NullInt64
		durDiff     sql.NullFloat64
	)
	if err := row.Scan(&r.ID, &r.Workflow, &started, &finished, &distance, &durDiff, &r.GroupCount, &r.Notes); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return nil, fmt.Errorf("parsing started_at %q: %w", started, err)
	}
	r.StartedAt = t
	r.FinishedAt = scanTime(finished)
	if distance.Valid {
		v := int(distance.Int64)
		r.Distance = &v
	}
	if durDiff.Valid {
		v := durDiff.Float64
		r.DurationDiff = &v
	}
	return &r, nil
}

// nullableInt converts *int → sql.NullInt64. nil → NULL.
func nullableInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

// nullableFloat converts *float64 → sql.NullFloat64. nil → NULL.
func nullableFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

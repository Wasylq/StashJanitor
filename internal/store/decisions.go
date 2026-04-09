package store

import (
	"context"
	"database/sql"
	"errors"
)

// PutUserDecision upserts a persistent user override. Returns no error if
// the row already existed; the new values replace the old.
func (s *Store) PutUserDecision(ctx context.Context, d UserDecision) error {
	if d.Key == "" {
		return errors.New("PutUserDecision: empty key")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_decisions (key, workflow, decision, keeper_id, decided_at, notes)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   workflow = excluded.workflow,
		   decision = excluded.decision,
		   keeper_id = excluded.keeper_id,
		   decided_at = excluded.decided_at,
		   notes = excluded.notes`,
		d.Key, d.Workflow, d.Decision,
		nullableString(d.KeeperID),
		nowRFC3339(),
		nullableString(d.Notes),
	)
	return err
}

// GetUserDecision returns the override stored under the given key, or
// ErrNotFound if there is no row.
func (s *Store) GetUserDecision(ctx context.Context, key string) (*UserDecision, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT key, workflow, decision, COALESCE(keeper_id,''), decided_at, COALESCE(notes,'')
		 FROM user_decisions WHERE key = ?`,
		key,
	)
	var d UserDecision
	var decidedAt string
	if err := row.Scan(&d.Key, &d.Workflow, &d.Decision, &d.KeeperID, &decidedAt, &d.Notes); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t := scanTime(sql.NullString{String: decidedAt, Valid: true})
	if t != nil {
		d.DecidedAt = *t
	}
	return &d, nil
}

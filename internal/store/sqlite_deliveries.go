package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NextDelivery reads a due item for the server's single queue worker.
func (s *SQLiteStore) NextDelivery(ctx context.Context, now time.Time) (DeliveryRecord, error) {
	var rec DeliveryRecord
	var received string
	err := s.db.QueryRowContext(ctx, `SELECT d.delivery_id, d.event, d.received_at, p.payload
 FROM pending_webhooks p JOIN webhook_deliveries d USING (delivery_id)
 WHERE p.retry_at <= ? ORDER BY p.rowid LIMIT 1`, now.Unix()).Scan(&rec.DeliveryID, &rec.Event, &received, &rec.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, ErrNotFound
	}
	if err == nil {
		rec.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
	}
	return rec, err
}

func (s *SQLiteStore) FinishDelivery(ctx context.Context, id string, succeeded bool) error {
	if succeeded {
		_, err := s.db.ExecContext(ctx, "DELETE FROM pending_webhooks WHERE delivery_id = ?", id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE pending_webhooks SET attempts = attempts+1,
 retry_at = ? + MIN(300, 5 << MIN(attempts,6)) WHERE delivery_id = ?`, time.Now().Unix(), id)
	return err
}

const registerInstallationSQL = `INSERT INTO installation_repositories (repo_full_name,installation_id) VALUES (?,?)
 ON CONFLICT(repo_full_name) DO UPDATE SET installation_id=excluded.installation_id`

func (s *SQLiteStore) RegisterInstallationRepository(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, registerInstallationSQL, name, id)
	return err
}

func (s *SQLiteStore) ListInstallationRepositories(ctx context.Context, id int64) ([]string, error) {
	// Existing databases may predate the installation map.
	rows, err := s.db.QueryContext(ctx, `SELECT repo_full_name FROM installation_repositories WHERE installation_id=?
 UNION SELECT repo_full_name FROM pull_requests WHERE installation_id=?
 AND repo_full_name NOT IN (SELECT repo_full_name FROM installation_repositories)`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

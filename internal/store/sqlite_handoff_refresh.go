package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Coalesce refreshes by PR. A concurrent change increments the version so
// finishing an earlier publication cannot discard a newer request.
const handoffRefreshSQL = `
CREATE TABLE IF NOT EXISTS handoff_refreshes (
 repo_full_name TEXT NOT NULL, pr_number INTEGER NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, retry_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(repo_full_name,pr_number)
);
CREATE TRIGGER IF NOT EXISTS artifact_refreshes_handoff AFTER INSERT ON artifacts BEGIN
 INSERT INTO handoff_refreshes(repo_full_name,pr_number)
 SELECT repo_full_name,pr_number FROM pull_requests WHERE repo_full_name=NEW.repo_full_name
 AND (pr_number=NEW.pr_number OR head_sha=NEW.producer_rev)
 ON CONFLICT(repo_full_name,pr_number) DO UPDATE SET version=version+1;
END;
CREATE TRIGGER IF NOT EXISTS check_insert_refreshes_handoff AFTER INSERT ON external_check_runs BEGIN
 INSERT INTO handoff_refreshes(repo_full_name,pr_number)
 SELECT repo_full_name,pr_number FROM pull_requests WHERE repo_full_name=NEW.repo_full_name
 AND head_sha=NEW.head_sha
 ON CONFLICT(repo_full_name,pr_number) DO UPDATE SET version=version+1;
END;
CREATE TRIGGER IF NOT EXISTS check_update_refreshes_handoff AFTER UPDATE ON external_check_runs BEGIN
 INSERT INTO handoff_refreshes(repo_full_name,pr_number)
 SELECT repo_full_name,pr_number FROM pull_requests WHERE repo_full_name=NEW.repo_full_name
 AND head_sha=NEW.head_sha
 ON CONFLICT(repo_full_name,pr_number) DO UPDATE SET version=version+1;
END;
CREATE TRIGGER IF NOT EXISTS validation_refreshes_handoff AFTER INSERT ON validation_checks BEGIN
 INSERT INTO handoff_refreshes(repo_full_name,pr_number)
 SELECT repo_full_name,pr_number FROM pull_requests WHERE repo_full_name=NEW.repo_full_name AND pr_number=NEW.pr_number
 ON CONFLICT(repo_full_name,pr_number) DO UPDATE SET version=version+1;
END;
`

type HandoffRefresh struct {
	Repo    string
	PR      int
	Version int64
}

func (s *SQLiteStore) NextHandoffRefresh(ctx context.Context) (HandoffRefresh, error) {
	var item HandoffRefresh
	err := s.db.QueryRowContext(ctx, `SELECT repo_full_name,pr_number,version FROM handoff_refreshes WHERE retry_at <= ? ORDER BY rowid LIMIT 1`, time.Now().Unix()).Scan(&item.Repo, &item.PR, &item.Version)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return item, err
}
func (s *SQLiteStore) FinishHandoffRefresh(ctx context.Context, item HandoffRefresh, success bool) error {
	if success {
		_, err := s.db.ExecContext(ctx, `DELETE FROM handoff_refreshes WHERE repo_full_name=? AND pr_number=? AND version=?`, item.Repo, item.PR, item.Version)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE handoff_refreshes SET retry_at=? WHERE repo_full_name=? AND pr_number=?`, time.Now().Add(time.Minute).Unix(), item.Repo, item.PR)
	return err
}

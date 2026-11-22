package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS webhook_deliveries (
	delivery_id    TEXT PRIMARY KEY,
	event          TEXT NOT NULL,
	action         TEXT NOT NULL,
	repo_full_name TEXT NOT NULL,
	received_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pull_requests (
	repo_full_name  TEXT NOT NULL,
	pr_number       INTEGER NOT NULL,
	title           TEXT NOT NULL,
	state           TEXT NOT NULL,
	draft           INTEGER NOT NULL,
	head_sha        TEXT NOT NULL,
	base_ref        TEXT NOT NULL,
	html_url        TEXT NOT NULL,
	installation_id INTEGER NOT NULL,
	check_run_id    INTEGER NOT NULL DEFAULT 0,
	check_run_head  TEXT NOT NULL DEFAULT '',
	last_action     TEXT NOT NULL,
	observed_at     TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, pr_number)
);
`

// SQLiteStore implements Store using an embedded pure-Go SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (or creates) a SQLite database at path and applies migrations.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db directory %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// Single writer connection prevents SQLITE_BUSY errors under concurrent webhook workers.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply sqlite schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) RecordDelivery(ctx context.Context, rec DeliveryRecord) (bool, error) {
	receivedAt := rec.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}

	res, err := s.db.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO webhook_deliveries (delivery_id, event, action, repo_full_name, received_at)
		 VALUES (?, ?, ?, ?, ?)`,
		rec.DeliveryID,
		rec.Event,
		rec.Action,
		rec.RepoFullName,
		receivedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, fmt.Errorf("record delivery %s: %w", rec.DeliveryID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected for delivery %s: %w", rec.DeliveryID, err)
	}
	return affected > 0, nil
}

func (s *SQLiteStore) UpsertPullRequest(ctx context.Context, rec PullRequestRecord) error {
	observedAt := rec.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	draft := 0
	if rec.Draft {
		draft = 1
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO pull_requests (
			repo_full_name, pr_number, title, state, draft, head_sha, base_ref,
			html_url, installation_id, check_run_id, check_run_head, last_action, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, pr_number) DO UPDATE SET
			title = excluded.title,
			state = excluded.state,
			draft = excluded.draft,
			head_sha = excluded.head_sha,
			base_ref = excluded.base_ref,
			html_url = excluded.html_url,
			installation_id = excluded.installation_id,
			last_action = excluded.last_action,
			observed_at = excluded.observed_at`,
		rec.RepoFullName,
		rec.Number,
		rec.Title,
		rec.State,
		draft,
		rec.HeadSHA,
		rec.BaseRef,
		rec.HTMLURL,
		rec.InstallationID,
		rec.CheckRunID,
		rec.CheckRunHead,
		rec.LastAction,
		observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert pull request %s#%d: %w", rec.RepoFullName, rec.Number, err)
	}
	return nil
}

func (s *SQLiteStore) RecordHandoffCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE pull_requests
		 SET check_run_id = ?, check_run_head = ?
		 WHERE repo_full_name = ? AND pr_number = ?`,
		checkRunID,
		headSHA,
		repoFullName,
		prNumber,
	)
	if err != nil {
		return fmt.Errorf("record handoff check run %s#%d: %w", repoFullName, prNumber, err)
	}
	return nil
}

func (s *SQLiteStore) GetPullRequest(ctx context.Context, repoFullName string, prNumber int) (PullRequestRecord, error) {
	var (
		rec        PullRequestRecord
		draft      int
		observedAt string
	)
	err := s.db.QueryRowContext(
		ctx,
		`SELECT repo_full_name, pr_number, title, state, draft, head_sha, base_ref,
		        html_url, installation_id, check_run_id, check_run_head, last_action, observed_at
		 FROM pull_requests
		 WHERE repo_full_name = ? AND pr_number = ?`,
		repoFullName,
		prNumber,
	).Scan(
		&rec.RepoFullName,
		&rec.Number,
		&rec.Title,
		&rec.State,
		&draft,
		&rec.HeadSHA,
		&rec.BaseRef,
		&rec.HTMLURL,
		&rec.InstallationID,
		&rec.CheckRunID,
		&rec.CheckRunHead,
		&rec.LastAction,
		&observedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequestRecord{}, ErrNotFound
	}
	if err != nil {
		return PullRequestRecord{}, fmt.Errorf("get pull request %s#%d: %w", repoFullName, prNumber, err)
	}

	rec.Draft = draft != 0
	parsedTime, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return PullRequestRecord{}, fmt.Errorf("parse observed_at %q: %w", observedAt, err)
	}
	rec.ObservedAt = parsedTime
	return rec, nil
}

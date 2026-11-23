package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

CREATE TABLE IF NOT EXISTS installation_repositories (
 repo_full_name TEXT PRIMARY KEY, installation_id INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pending_webhooks (
 delivery_id TEXT PRIMARY KEY,
 payload BLOB NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER IF NOT EXISTS purge_pending_webhook AFTER DELETE ON webhook_deliveries
BEGIN DELETE FROM pending_webhooks WHERE delivery_id = OLD.delivery_id; END;

CREATE TABLE IF NOT EXISTS pull_requests (
	repo_full_name         TEXT NOT NULL,
	pr_number              INTEGER NOT NULL,
	title                  TEXT NOT NULL,
	state                  TEXT NOT NULL,
	draft                  INTEGER NOT NULL,
	head_sha               TEXT NOT NULL,
	base_ref               TEXT NOT NULL,
	html_url               TEXT NOT NULL,
	author                 TEXT NOT NULL DEFAULT '',
	body                   TEXT NOT NULL DEFAULT '',
	labels_json            TEXT NOT NULL DEFAULT '[]',
	linked_issues_json     TEXT NOT NULL DEFAULT '[]',
	paths_json             TEXT NOT NULL DEFAULT '[]',
	merged                 INTEGER NOT NULL DEFAULT 0,
	merge_commit_sha       TEXT NOT NULL DEFAULT '',
	revision_kind          TEXT NOT NULL DEFAULT 'pr_head',
	installation_id        INTEGER NOT NULL,
	check_run_id           INTEGER NOT NULL DEFAULT 0,
	check_run_head         TEXT NOT NULL DEFAULT '',
	awareness_check_run_id INTEGER NOT NULL DEFAULT 0,
	awareness_check_head   TEXT NOT NULL DEFAULT '',
	awareness_conclusion   TEXT NOT NULL DEFAULT '',
	last_action            TEXT NOT NULL,
	observed_at            TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, pr_number)
);

CREATE TABLE IF NOT EXISTS pull_request_revisions (
	repo_full_name         TEXT NOT NULL,
	pr_number              INTEGER NOT NULL,
	head_sha               TEXT NOT NULL,
	base_ref               TEXT NOT NULL,
	action                 TEXT NOT NULL,
	revision_kind          TEXT NOT NULL DEFAULT 'pr_head',
	check_run_id           INTEGER NOT NULL DEFAULT 0,
	awareness_check_run_id INTEGER NOT NULL DEFAULT 0,
	awareness_conclusion   TEXT NOT NULL DEFAULT '',
	superseded_by_sha      TEXT NOT NULL DEFAULT '',
	observed_at            TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, pr_number, head_sha)
);

CREATE TABLE IF NOT EXISTS pull_request_reviews (
	repo_full_name TEXT NOT NULL,
	pr_number      INTEGER NOT NULL,
	review_id      INTEGER NOT NULL,
	reviewer       TEXT NOT NULL,
	state          TEXT NOT NULL,
	commit_sha     TEXT NOT NULL,
	body_excerpt   TEXT NOT NULL DEFAULT '',
	html_url       TEXT NOT NULL DEFAULT '',
	submitted_at   TEXT NOT NULL,
	observed_at    TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, pr_number, review_id)
);

CREATE TABLE IF NOT EXISTS external_check_runs (
	repo_full_name TEXT NOT NULL,
	head_sha       TEXT NOT NULL,
	check_run_id   INTEGER NOT NULL,
	name           TEXT NOT NULL,
	status         TEXT NOT NULL,
	conclusion     TEXT NOT NULL DEFAULT '',
	details_url    TEXT NOT NULL DEFAULT '',
	app_id         INTEGER NOT NULL DEFAULT 0,
	observed_at    TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, head_sha, check_run_id)
);

CREATE TABLE IF NOT EXISTS artifacts (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_full_name  TEXT NOT NULL,
	artifact_key    TEXT NOT NULL,
	version         INTEGER NOT NULL,
	producer_worker TEXT NOT NULL,
	producer_rev    TEXT NOT NULL,
	pr_number       INTEGER NOT NULL DEFAULT 0,
	command         TEXT NOT NULL DEFAULT '',
	content_type    TEXT NOT NULL DEFAULT 'text/plain',
	payload         TEXT NOT NULL,
	checksum        TEXT NOT NULL,
	summary         TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL,
	UNIQUE (repo_full_name, artifact_key, version)
);

CREATE TABLE IF NOT EXISTS artifact_subscriptions (
	repo_full_name     TEXT NOT NULL,
	artifact_key       TEXT NOT NULL,
	worker_id          TEXT NOT NULL,
	context_generation INTEGER NOT NULL,
	consumed_version   INTEGER NOT NULL DEFAULT 0,
	consumer_rev       TEXT NOT NULL DEFAULT '',
	subscribed_at      TEXT NOT NULL,
	consumed_at        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (repo_full_name, artifact_key, worker_id, context_generation)
);

CREATE TABLE IF NOT EXISTS validation_checks (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_full_name   TEXT NOT NULL,
	job_name         TEXT NOT NULL,
	pr_number        INTEGER NOT NULL DEFAULT 0,
	producer_rev     TEXT NOT NULL,
	consumer_rev     TEXT NOT NULL DEFAULT '',
	inputs_hash      TEXT NOT NULL,
	toolchain        TEXT NOT NULL,
	flags            TEXT NOT NULL DEFAULT '',
	environment      TEXT NOT NULL,
	artifact_key     TEXT NOT NULL DEFAULT '',
	artifact_version INTEGER NOT NULL DEFAULT 0,
	status           TEXT NOT NULL,
	conclusion       TEXT NOT NULL DEFAULT '',
	flaky            INTEGER NOT NULL DEFAULT 0,
	output_summary   TEXT NOT NULL DEFAULT '',
	superseded_by_id INTEGER NOT NULL DEFAULT 0,
	started_at       TEXT NOT NULL,
	completed_at     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS proposals (
	repo_full_name     TEXT NOT NULL,
	proposal_key       TEXT NOT NULL,
	source_type        TEXT NOT NULL,
	source_ref         TEXT NOT NULL,
	source_url         TEXT NOT NULL DEFAULT '',
	title              TEXT NOT NULL,
	owner              TEXT NOT NULL DEFAULT '',
	state              TEXT NOT NULL,
	superseded_by      TEXT NOT NULL DEFAULT '',
	version            INTEGER NOT NULL DEFAULT 1,
	scope_paths_json   TEXT NOT NULL DEFAULT '[]',
	interfaces_json    TEXT NOT NULL DEFAULT '[]',
	linked_issues_json TEXT NOT NULL DEFAULT '[]',
	summary            TEXT NOT NULL DEFAULT '',
	excerpt            TEXT NOT NULL DEFAULT '',
	visibility         TEXT NOT NULL DEFAULT 'public',
	updated_at         TEXT NOT NULL,
	observed_at        TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, proposal_key)
);

CREATE TABLE IF NOT EXISTS worker_scopes (
	repo_full_name     TEXT NOT NULL,
	worker_id          TEXT NOT NULL,
	context_generation INTEGER NOT NULL,
	parent_worker_id   TEXT NOT NULL DEFAULT '',
	role               TEXT NOT NULL DEFAULT '',
	pr_number          INTEGER NOT NULL DEFAULT 0,
	base_rev           TEXT NOT NULL DEFAULT '',
	issue_links_json   TEXT NOT NULL DEFAULT '[]',
	paths_json         TEXT NOT NULL DEFAULT '[]',
	interfaces_json    TEXT NOT NULL DEFAULT '[]',
	registered_at      TEXT NOT NULL,
	expires_at         TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, worker_id, context_generation)
);

CREATE TABLE IF NOT EXISTS delivery_receipts (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_full_name     TEXT NOT NULL,
	worker_id          TEXT NOT NULL,
	context_generation INTEGER NOT NULL,
	evidence_set_hash  TEXT NOT NULL,
	items_json         TEXT NOT NULL DEFAULT '[]',
	delivered_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_delivery_receipts_worker
	ON delivery_receipts (repo_full_name, worker_id, context_generation, id DESC);

CREATE TABLE IF NOT EXISTS awareness_evaluations (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_full_name     TEXT NOT NULL,
	pr_number          INTEGER NOT NULL DEFAULT 0,
	head_sha           TEXT NOT NULL,
	revision_kind      TEXT NOT NULL DEFAULT 'pr_head',
	worker_id          TEXT NOT NULL DEFAULT '',
	context_generation INTEGER NOT NULL DEFAULT 0,
	evidence_set_hash  TEXT NOT NULL,
	verdict            TEXT NOT NULL,
	complete           INTEGER NOT NULL DEFAULT 0,
	reason             TEXT NOT NULL DEFAULT '',
	check_run_id       INTEGER NOT NULL DEFAULT 0,
	indexed_at         TEXT NOT NULL,
	validated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_awareness_evaluations_sha
	ON awareness_evaluations (repo_full_name, head_sha, id DESC);

CREATE TABLE IF NOT EXISTS overlap_acknowledgments (
	repo_full_name  TEXT NOT NULL,
	pr_a            INTEGER NOT NULL,
	pr_b            INTEGER NOT NULL,
	disposition     TEXT NOT NULL,
	actor           TEXT NOT NULL DEFAULT '',
	note            TEXT NOT NULL DEFAULT '',
	acknowledged_at TEXT NOT NULL,
	PRIMARY KEY (repo_full_name, pr_a, pr_b)
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
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create db directory %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// Single writer connection prevents SQLITE_BUSY errors under concurrent webhook workers
	// and serializes atomic scope registration transactions.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply sqlite schema: %w", err)
	}

	if err := migratePullRequestsColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate pull_requests columns: %w", err)
	}

	// Earlier versions encoded unknown paths as []; invalidate those rows once.
	if err := migrateUnknownPaths(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate unknown pull request paths: %w", err)
	}

	if _, err := db.Exec(proposalAwarenessRefreshSQL + handoffRefreshSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply proposal awareness refresh schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func migrateUnknownPaths(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations (name) VALUES ('unknown_pr_paths_v1')`)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		if _, err := tx.Exec(`UPDATE pull_requests SET paths_json = 'null' WHERE paths_json = '[]'`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migratePullRequestsColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(pull_requests)`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name, ctyp string
			notNull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctyp, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	additions := []struct {
		name string
		ddl  string
	}{
		{"author", "ALTER TABLE pull_requests ADD COLUMN author TEXT NOT NULL DEFAULT ''"},
		{"body", "ALTER TABLE pull_requests ADD COLUMN body TEXT NOT NULL DEFAULT ''"},
		{"labels_json", "ALTER TABLE pull_requests ADD COLUMN labels_json TEXT NOT NULL DEFAULT '[]'"},
		{"linked_issues_json", "ALTER TABLE pull_requests ADD COLUMN linked_issues_json TEXT NOT NULL DEFAULT '[]'"},
		{"paths_json", "ALTER TABLE pull_requests ADD COLUMN paths_json TEXT NOT NULL DEFAULT '[]'"},
		{"merged", "ALTER TABLE pull_requests ADD COLUMN merged INTEGER NOT NULL DEFAULT 0"},
		{"merge_commit_sha", "ALTER TABLE pull_requests ADD COLUMN merge_commit_sha TEXT NOT NULL DEFAULT ''"},
		{"revision_kind", "ALTER TABLE pull_requests ADD COLUMN revision_kind TEXT NOT NULL DEFAULT 'pr_head'"},
		{"awareness_check_run_id", "ALTER TABLE pull_requests ADD COLUMN awareness_check_run_id INTEGER NOT NULL DEFAULT 0"},
		{"awareness_check_head", "ALTER TABLE pull_requests ADD COLUMN awareness_check_head TEXT NOT NULL DEFAULT ''"},
		{"awareness_conclusion", "ALTER TABLE pull_requests ADD COLUMN awareness_conclusion TEXT NOT NULL DEFAULT ''"},
	}

	for _, col := range additions {
		if !existing[col.name] {
			if _, err := db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) RecordDelivery(ctx context.Context, rec DeliveryRecord) (bool, error) {
	receivedAt := rec.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(
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
	if affected > 0 && rec.InstallationID > 0 && rec.RepoFullName != "" {
		if _, err := tx.ExecContext(ctx, registerInstallationSQL, rec.RepoFullName, rec.InstallationID); err != nil {
			return false, err
		}
	}
	if affected > 0 && rec.Payload != nil {
		if _, err = tx.ExecContext(ctx, "INSERT INTO pending_webhooks(delivery_id,payload) VALUES (?,?)", rec.DeliveryID, rec.Payload); err != nil {
			return false, err
		}
	}
	return affected > 0, tx.Commit()
}

func (s *SQLiteStore) UpsertPullRequest(ctx context.Context, rec PullRequestRecord) error {
	observedAt := rec.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	draft := boolToInt(rec.Draft)
	merged := boolToInt(rec.Merged)
	revKind := rec.RevisionKind
	if revKind == "" {
		revKind = "pr_head"
	}

	labelsJSON := marshalJSONOrDefault(rec.Labels, "[]")
	issuesJSON := marshalJSONOrDefault(rec.LinkedIssues, "[]")
	pathsJSON := marshalJSONOrDefault(rec.Paths, "null")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert pull request %s#%d: %w", rec.RepoFullName, rec.Number, err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO pull_requests (
			repo_full_name, pr_number, title, state, draft, head_sha, base_ref,
			html_url, author, body, labels_json, linked_issues_json, paths_json,
			merged, merge_commit_sha, revision_kind, installation_id,
			check_run_id, check_run_head, awareness_check_run_id, awareness_check_head,
			awareness_conclusion, last_action, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, pr_number) DO UPDATE SET
			title = excluded.title,
			state = excluded.state,
			draft = excluded.draft,
			head_sha = excluded.head_sha,
			base_ref = excluded.base_ref,
			html_url = excluded.html_url,
			author = CASE WHEN excluded.author != '' THEN excluded.author ELSE pull_requests.author END,
			body = excluded.body,
			labels_json = excluded.labels_json,
			linked_issues_json = excluded.linked_issues_json,
			paths_json = excluded.paths_json,
			merged = excluded.merged,
			merge_commit_sha = CASE WHEN excluded.merge_commit_sha != '' THEN excluded.merge_commit_sha ELSE pull_requests.merge_commit_sha END,
			revision_kind = excluded.revision_kind,
			installation_id = CASE WHEN excluded.installation_id > 0 THEN excluded.installation_id ELSE pull_requests.installation_id END,
			check_run_id = CASE WHEN pull_requests.check_run_head = excluded.head_sha THEN pull_requests.check_run_id ELSE 0 END,
			check_run_head = CASE WHEN pull_requests.check_run_head = excluded.head_sha THEN pull_requests.check_run_head ELSE '' END,
			awareness_check_run_id = CASE WHEN pull_requests.awareness_check_head = excluded.head_sha THEN pull_requests.awareness_check_run_id ELSE 0 END,
			awareness_check_head = CASE WHEN pull_requests.awareness_check_head = excluded.head_sha THEN pull_requests.awareness_check_head ELSE '' END,
			awareness_conclusion = CASE WHEN pull_requests.awareness_check_head = excluded.head_sha THEN pull_requests.awareness_conclusion ELSE '' END,
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
		rec.Author,
		rec.Body,
		labelsJSON,
		issuesJSON,
		pathsJSON,
		merged,
		rec.MergeCommitSHA,
		revKind,
		rec.InstallationID,
		rec.CheckRunID,
		rec.CheckRunHead,
		rec.AwarenessCheckRunID,
		rec.AwarenessCheckHead,
		rec.AwarenessConclusion,
		rec.LastAction,
		observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert pull request %s#%d: %w", rec.RepoFullName, rec.Number, err)
	}

	if rec.HeadSHA != "" {
		// Mark prior non-superseded revisions for this PR as superseded by the new head SHA.
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE pull_request_revisions
			 SET superseded_by_sha = ?
			 WHERE repo_full_name = ? AND pr_number = ? AND head_sha != ? AND superseded_by_sha = ''`,
			rec.HeadSHA, rec.RepoFullName, rec.Number, rec.HeadSHA,
		); err != nil {
			return fmt.Errorf("supersede prior revisions %s#%d: %w", rec.RepoFullName, rec.Number, err)
		}

		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO pull_request_revisions (
				repo_full_name, pr_number, head_sha, base_ref, action, revision_kind, observed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_full_name, pr_number, head_sha) DO UPDATE SET
				base_ref = excluded.base_ref,
				action = excluded.action,
				revision_kind = excluded.revision_kind,
				superseded_by_sha = '',
				observed_at = excluded.observed_at`,
			rec.RepoFullName, rec.Number, rec.HeadSHA, rec.BaseRef, rec.LastAction, revKind, observedAt.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("upsert pr revision %s#%d@%s: %w", rec.RepoFullName, rec.Number, rec.HeadSHA, err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) RecordHandoffCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE pull_requests
		 SET check_run_id = ?, check_run_head = ?
		 WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ?`,
		checkRunID, headSHA, repoFullName, prNumber, headSHA,
	); err != nil {
		return fmt.Errorf("record handoff check run %s#%d: %w", repoFullName, prNumber, err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE pull_request_revisions
		 SET check_run_id = ?
		 WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ?`,
		checkRunID, repoFullName, prNumber, headSHA,
	); err != nil {
		return fmt.Errorf("record revision handoff check run %s#%d@%s: %w", repoFullName, prNumber, headSHA, err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) RecordAwarenessCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64, conclusion string) error {
	return s.recordAwarenessCheckRun(ctx, repoFullName, prNumber, headSHA, checkRunID, conclusion, nil)
}

func (s *SQLiteStore) recordAwarenessCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64, conclusion string, eval *AwarenessEvaluation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if prNumber > 0 {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE pull_requests
			 SET awareness_check_run_id = ?, awareness_check_head = ?, awareness_conclusion = ?
			 WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ?`,
			checkRunID, headSHA, conclusion, repoFullName, prNumber, headSHA,
		); err != nil {
			return fmt.Errorf("record awareness check run %s#%d: %w", repoFullName, prNumber, err)
		}

		if _, err := tx.ExecContext(
			ctx,
			`UPDATE pull_request_revisions
			 SET awareness_check_run_id = ?, awareness_conclusion = ?
			 WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ?`,
			checkRunID, conclusion, repoFullName, prNumber, headSHA,
		); err != nil {
			return fmt.Errorf("record revision awareness check run %s#%d@%s: %w", repoFullName, prNumber, headSHA, err)
		}
	}

	if eval != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE awareness_evaluations SET check_run_id = ? WHERE id = ?`, checkRunID, eval.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO awareness_publication_watermarks
				(repo_full_name, pr_number, head_sha, check_run_id, proposal_watermark)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(repo_full_name, pr_number, head_sha, check_run_id) DO UPDATE
				SET proposal_watermark = excluded.proposal_watermark`,
			eval.RepoFullName, eval.PRNumber, eval.HeadSHA, checkRunID, eval.ProposalWatermark); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetPullRequest(ctx context.Context, repoFullName string, prNumber int) (PullRequestRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT repo_full_name, pr_number, title, state, draft, head_sha, base_ref,
		        html_url, author, body, labels_json, linked_issues_json, paths_json,
		        merged, merge_commit_sha, revision_kind, installation_id,
		        check_run_id, check_run_head, awareness_check_run_id, awareness_check_head,
		        awareness_conclusion, last_action, observed_at
		 FROM pull_requests
		 WHERE repo_full_name = ? AND pr_number = ?`,
		repoFullName,
		prNumber,
	)
	rec, err := scanPullRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequestRecord{}, ErrNotFound
	}
	if err != nil {
		return PullRequestRecord{}, fmt.Errorf("get pull request %s#%d: %w", repoFullName, prNumber, err)
	}
	return rec, nil
}

func (s *SQLiteStore) ListOpenPullRequests(ctx context.Context, repoFullName string) ([]PullRequestRecord, error) {
	return listOpenPullRequestsTx(ctx, s.db, repoFullName)
}

type rowScanner interface {
	Scan(dest ...any) error
}

type queryable interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listOpenPullRequestsTx(ctx context.Context, q queryable, repoFullName string) ([]PullRequestRecord, error) {
	rows, err := q.QueryContext(
		ctx,
		`SELECT repo_full_name, pr_number, title, state, draft, head_sha, base_ref,
		        html_url, author, body, labels_json, linked_issues_json, paths_json,
		        merged, merge_commit_sha, revision_kind, installation_id,
		        check_run_id, check_run_head, awareness_check_run_id, awareness_check_head,
		        awareness_conclusion, last_action, observed_at
		 FROM pull_requests
		 WHERE repo_full_name = ? AND state = 'open'
		 ORDER BY pr_number ASC`,
		repoFullName,
	)
	if err != nil {
		return nil, fmt.Errorf("list open pull requests %s: %w", repoFullName, err)
	}
	defer rows.Close()

	var out []PullRequestRecord
	for rows.Next() {
		rec, err := scanPullRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanPullRequest(s rowScanner) (PullRequestRecord, error) {
	var (
		rec                             PullRequestRecord
		draft, merged                   int
		labelsJSON, issuesJSON, pathsJS string
		observedAt                      string
	)
	err := s.Scan(
		&rec.RepoFullName,
		&rec.Number,
		&rec.Title,
		&rec.State,
		&draft,
		&rec.HeadSHA,
		&rec.BaseRef,
		&rec.HTMLURL,
		&rec.Author,
		&rec.Body,
		&labelsJSON,
		&issuesJSON,
		&pathsJS,
		&merged,
		&rec.MergeCommitSHA,
		&rec.RevisionKind,
		&rec.InstallationID,
		&rec.CheckRunID,
		&rec.CheckRunHead,
		&rec.AwarenessCheckRunID,
		&rec.AwarenessCheckHead,
		&rec.AwarenessConclusion,
		&rec.LastAction,
		&observedAt,
	)
	if err != nil {
		return PullRequestRecord{}, err
	}

	rec.Draft = draft != 0
	rec.Merged = merged != 0
	_ = json.Unmarshal([]byte(labelsJSON), &rec.Labels)
	_ = json.Unmarshal([]byte(issuesJSON), &rec.LinkedIssues)
	_ = json.Unmarshal([]byte(pathsJS), &rec.Paths)

	parsedTime, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return PullRequestRecord{}, fmt.Errorf("parse observed_at %q: %w", observedAt, err)
	}
	rec.ObservedAt = parsedTime
	return rec, nil
}

func (s *SQLiteStore) ListPullRequestRevisions(ctx context.Context, repoFullName string, prNumber int) ([]PullRequestRevision, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT repo_full_name, pr_number, head_sha, base_ref, action, revision_kind,
		        check_run_id, awareness_check_run_id, awareness_conclusion, superseded_by_sha, observed_at
		 FROM pull_request_revisions
		 WHERE repo_full_name = ? AND pr_number = ?
		 ORDER BY observed_at DESC`,
		repoFullName, prNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("list pr revisions %s#%d: %w", repoFullName, prNumber, err)
	}
	defer rows.Close()

	var out []PullRequestRevision
	for rows.Next() {
		var rev PullRequestRevision
		var obs string
		if err := rows.Scan(
			&rev.RepoFullName, &rev.PRNumber, &rev.HeadSHA, &rev.BaseRef,
			&rev.Action, &rev.RevisionKind, &rev.CheckRunID,
			&rev.AwarenessCheckRunID, &rev.AwarenessConclusion,
			&rev.SupersededBySHA, &obs,
		); err != nil {
			return nil, err
		}
		rev.ObservedAt, _ = time.Parse(time.RFC3339Nano, obs)
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertReview(ctx context.Context, rec ReviewRecord) error {
	submittedAt := rec.SubmittedAt.UTC()
	if submittedAt.IsZero() {
		submittedAt = time.Now().UTC()
	}
	observedAt := rec.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO pull_request_reviews (
			repo_full_name, pr_number, review_id, reviewer, state, commit_sha,
			body_excerpt, html_url, submitted_at, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, pr_number, review_id) DO UPDATE SET
			reviewer = excluded.reviewer,
			state = excluded.state,
			commit_sha = excluded.commit_sha,
			body_excerpt = excluded.body_excerpt,
			html_url = excluded.html_url,
			submitted_at = excluded.submitted_at,
			observed_at = excluded.observed_at`,
		rec.RepoFullName, rec.PRNumber, rec.ReviewID, rec.Reviewer, rec.State,
		rec.CommitSHA, rec.BodyExcerpt, rec.HTMLURL,
		submittedAt.Format(time.RFC3339Nano), observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert review %s#%d/%d: %w", rec.RepoFullName, rec.PRNumber, rec.ReviewID, err)
	}
	return nil
}

func (s *SQLiteStore) ListReviews(ctx context.Context, repoFullName string, prNumber int) ([]ReviewRecord, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT repo_full_name, pr_number, review_id, reviewer, state, commit_sha,
		        body_excerpt, html_url, submitted_at, observed_at
		 FROM pull_request_reviews
		 WHERE repo_full_name = ? AND pr_number = ?
		 ORDER BY submitted_at DESC, review_id DESC`,
		repoFullName, prNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("list reviews %s#%d: %w", repoFullName, prNumber, err)
	}
	defer rows.Close()

	var out []ReviewRecord
	for rows.Next() {
		var r ReviewRecord
		var sub, obs string
		if err := rows.Scan(
			&r.RepoFullName, &r.PRNumber, &r.ReviewID, &r.Reviewer,
			&r.State, &r.CommitSHA, &r.BodyExcerpt, &r.HTMLURL, &sub, &obs,
		); err != nil {
			return nil, err
		}
		r.SubmittedAt, _ = time.Parse(time.RFC3339Nano, sub)
		r.ObservedAt, _ = time.Parse(time.RFC3339Nano, obs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertExternalCheckRun(ctx context.Context, rec ExternalCheckRun) error {
	observedAt := rec.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO external_check_runs (
			repo_full_name, head_sha, check_run_id, name, status, conclusion, details_url, app_id, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, head_sha, check_run_id) DO UPDATE SET
			name = excluded.name,
			status = excluded.status,
			conclusion = excluded.conclusion,
			details_url = excluded.details_url,
			app_id = excluded.app_id,
			observed_at = excluded.observed_at`,
		rec.RepoFullName, rec.HeadSHA, rec.CheckRunID, rec.Name, rec.Status,
		rec.Conclusion, rec.DetailsURL, rec.AppID, observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert external check run %s@%s/%d: %w", rec.RepoFullName, rec.HeadSHA, rec.CheckRunID, err)
	}
	return nil
}

func (s *SQLiteStore) ListExternalCheckRuns(ctx context.Context, repoFullName, headSHA string) ([]ExternalCheckRun, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT repo_full_name, head_sha, check_run_id, name, status, conclusion, details_url, app_id, observed_at
		 FROM external_check_runs
		 WHERE repo_full_name = ? AND head_sha = ?
		 ORDER BY name ASC, check_run_id DESC`,
		repoFullName, headSHA,
	)
	if err != nil {
		return nil, fmt.Errorf("list external check runs %s@%s: %w", repoFullName, headSHA, err)
	}
	defer rows.Close()

	var out []ExternalCheckRun
	for rows.Next() {
		var r ExternalCheckRun
		var obs string
		if err := rows.Scan(
			&r.RepoFullName, &r.HeadSHA, &r.CheckRunID, &r.Name,
			&r.Status, &r.Conclusion, &r.DetailsURL, &r.AppID, &obs,
		); err != nil {
			return nil, err
		}
		r.ObservedAt, _ = time.Parse(time.RFC3339Nano, obs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) PurgeRepository(ctx context.Context, repoFullName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge %s: %w", repoFullName, err)
	}
	defer func() { _ = tx.Rollback() }()

	tables := []string{
		"handoff_refreshes",
		"proposal_awareness_refresh",
		"awareness_publication_watermarks",
		"installation_repositories",
		"webhook_deliveries",
		"pull_requests",
		"pull_request_revisions",
		"pull_request_reviews",
		"external_check_runs",
		"artifacts",
		"artifact_subscriptions",
		"validation_checks",
		"proposals",
		"worker_scopes",
		"delivery_receipts",
		"awareness_evaluations",
		"overlap_acknowledgments",
	}
	for _, tbl := range tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+tbl+" WHERE repo_full_name = ?", repoFullName); err != nil {
			return fmt.Errorf("purge %s from %s: %w", repoFullName, tbl, err)
		}
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func marshalJSONOrDefault(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return fallback
	}
	return string(b)
}

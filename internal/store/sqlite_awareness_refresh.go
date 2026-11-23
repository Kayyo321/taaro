package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const proposalAwarenessRefreshSQL = `
CREATE TABLE IF NOT EXISTS proposal_awareness_refresh (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_full_name TEXT NOT NULL,
	previous_scope TEXT NOT NULL,
	current_scope TEXT NOT NULL,
	retry_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS awareness_publication_watermarks (
	repo_full_name TEXT NOT NULL,
	pr_number INTEGER NOT NULL,
	head_sha TEXT NOT NULL,
	check_run_id INTEGER NOT NULL,
	proposal_watermark INTEGER NOT NULL,
	PRIMARY KEY (repo_full_name, pr_number, head_sha, check_run_id)
);

CREATE TRIGGER IF NOT EXISTS enqueue_inserted_proposal_awareness_refresh
AFTER INSERT ON proposals
BEGIN
	INSERT INTO proposal_awareness_refresh (repo_full_name, previous_scope, current_scope)
	VALUES (NEW.repo_full_name, '{}',
		json_object('state', NEW.state, 'scope_paths', json(NEW.scope_paths_json), 'linked_issues', json(NEW.linked_issues_json)));
END;

CREATE TRIGGER IF NOT EXISTS enqueue_revised_proposal_awareness_refresh
AFTER UPDATE OF version ON proposals
WHEN NEW.version != OLD.version
BEGIN
	INSERT INTO proposal_awareness_refresh (repo_full_name, previous_scope, current_scope)
	VALUES (NEW.repo_full_name,
		json_object('state', OLD.state, 'scope_paths', json(OLD.scope_paths_json), 'linked_issues', json(OLD.linked_issues_json)),
		json_object('state', NEW.state, 'scope_paths', json(NEW.scope_paths_json), 'linked_issues', json(NEW.linked_issues_json)));
END;
`

// ProposalAwarenessRefresh records the old and new indexed scopes of a proposal revision.
type ProposalAwarenessRefresh struct {
	ID           int64
	RepoFullName string
	Previous     ProposalRecord
	Current      ProposalRecord
}

func (s *SQLiteStore) NextProposalAwarenessRefresh(ctx context.Context, now time.Time) (ProposalAwarenessRefresh, error) {
	var job ProposalAwarenessRefresh
	var previous, current string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repo_full_name, previous_scope, current_scope
		FROM proposal_awareness_refresh WHERE retry_at <= ? ORDER BY id LIMIT 1`, now.Unix()).
		Scan(&job.ID, &job.RepoFullName, &previous, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalAwarenessRefresh{}, ErrNotFound
	}
	if err != nil {
		return ProposalAwarenessRefresh{}, err
	}

	if err := json.Unmarshal([]byte(previous), &job.Previous); err != nil {
		return ProposalAwarenessRefresh{}, err
	}
	if err := json.Unmarshal([]byte(current), &job.Current); err != nil {
		return ProposalAwarenessRefresh{}, err
	}
	return job, nil
}

func (s *SQLiteStore) FinishProposalAwarenessRefresh(ctx context.Context, id int64, retryAt time.Time) error {
	if retryAt.IsZero() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM proposal_awareness_refresh WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE proposal_awareness_refresh SET retry_at = ? WHERE id = ?`, retryAt.Unix(), id)
	return err
}

// CurrentProposalAwarenessWatermark returns the latest committed proposal refresh ID,
// including already processed jobs.
func (s *SQLiteStore) CurrentProposalAwarenessWatermark(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'proposal_awareness_refresh'), 0)`).Scan(&id)
	return id, err
}

func (s *SQLiteStore) AwarenessPublicationWatermark(ctx context.Context, pr PullRequestRecord) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT proposal_watermark FROM awareness_publication_watermarks
		WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ? AND check_run_id = ?`,
		pr.RepoFullName, pr.Number, pr.HeadSHA, pr.AwarenessCheckRunID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *SQLiteStore) RecordAwarenessPublication(ctx context.Context, eval AwarenessEvaluation, checkID int64) error {
	return s.recordAwarenessCheckRun(ctx, eval.RepoFullName, eval.PRNumber, eval.HeadSHA, checkID, eval.Verdict, &eval)
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// UpsertProposal inserts or updates a proposal, incrementing its version whenever substantive fields or state change.
func (s *SQLiteStore) UpsertProposal(ctx context.Context, rec ProposalRecord) (ProposalRecord, error) {
	now := time.Now().UTC()
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	if rec.ObservedAt.IsZero() {
		rec.ObservedAt = now
	}
	if rec.Visibility == "" {
		rec.Visibility = "public"
	}
	if rec.State == "" {
		rec.State = "draft"
	}

	pathsJSON := marshalJSONOrDefault(rec.ScopePaths, "[]")
	ifacesJSON := marshalJSONOrDefault(rec.Interfaces, "[]")
	issuesJSON := marshalJSONOrDefault(rec.LinkedIssues, "[]")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProposalRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		prevSourceType, prevSourceRef, prevSourceURL, prevOwner string
		prevVer                                                 int
		prevTitle, prevState, prevSuper, prevVis                string
		prevPaths, prevIfaces, prevIssues, prevSum, prevEx      string
	)
	err = tx.QueryRowContext(
		ctx,
		`SELECT version, source_type, source_ref, source_url, owner, title, state, superseded_by, visibility,
		        scope_paths_json, interfaces_json, linked_issues_json, summary, excerpt
		 FROM proposals
		 WHERE repo_full_name = ? AND proposal_key = ?`,
		rec.RepoFullName, rec.ProposalKey,
	).Scan(&prevVer, &prevSourceType, &prevSourceRef, &prevSourceURL, &prevOwner, &prevTitle, &prevState, &prevSuper, &prevVis, &prevPaths, &prevIfaces, &prevIssues, &prevSum, &prevEx)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if rec.Version <= 0 {
			rec.Version = 1
		}
	case err != nil:
		return ProposalRecord{}, fmt.Errorf("lookup proposal %s/%s: %w", rec.RepoFullName, rec.ProposalKey, err)
	default:
		changed := prevSourceType != rec.SourceType ||
			prevSourceRef != rec.SourceRef ||
			prevSourceURL != rec.SourceURL ||
			prevOwner != rec.Owner ||
			prevTitle != rec.Title ||
			prevState != rec.State ||
			prevSuper != rec.SupersededBy ||
			prevVis != rec.Visibility ||
			prevPaths != pathsJSON ||
			prevIfaces != ifacesJSON ||
			prevIssues != issuesJSON ||
			prevSum != rec.Summary ||
			prevEx != rec.Excerpt
		if changed {
			rec.Version = prevVer + 1
		} else {
			rec.Version = prevVer
		}
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO proposals (
			repo_full_name, proposal_key, source_type, source_ref, source_url,
			title, owner, state, superseded_by, version,
			scope_paths_json, interfaces_json, linked_issues_json,
			summary, excerpt, visibility, updated_at, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, proposal_key) DO UPDATE SET
			source_type = excluded.source_type,
			source_ref = excluded.source_ref,
			source_url = excluded.source_url,
			title = excluded.title,
			owner = excluded.owner,
			state = excluded.state,
			superseded_by = excluded.superseded_by,
			version = excluded.version,
			scope_paths_json = excluded.scope_paths_json,
			interfaces_json = excluded.interfaces_json,
			linked_issues_json = excluded.linked_issues_json,
			summary = excluded.summary,
			excerpt = excluded.excerpt,
			visibility = excluded.visibility,
			updated_at = excluded.updated_at,
			observed_at = excluded.observed_at`,
		rec.RepoFullName, rec.ProposalKey, rec.SourceType, rec.SourceRef, rec.SourceURL,
		rec.Title, rec.Owner, rec.State, rec.SupersededBy, rec.Version,
		pathsJSON, ifacesJSON, issuesJSON,
		rec.Summary, rec.Excerpt, rec.Visibility,
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		rec.ObservedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return ProposalRecord{}, fmt.Errorf("upsert proposal %s/%s: %w", rec.RepoFullName, rec.ProposalKey, err)
	}

	if err := tx.Commit(); err != nil {
		return ProposalRecord{}, err
	}
	return rec, nil
}

func (s *SQLiteStore) ListProposals(ctx context.Context, repoFullName string) ([]ProposalRecord, error) {
	return listProposalsTx(ctx, s.db, repoFullName)
}

func listProposalsTx(ctx context.Context, q queryable, repoFullName string) ([]ProposalRecord, error) {
	rows, err := q.QueryContext(
		ctx,
		`SELECT repo_full_name, proposal_key, source_type, source_ref, source_url,
		        title, owner, state, superseded_by, version,
		        scope_paths_json, interfaces_json, linked_issues_json,
		        summary, excerpt, visibility, updated_at, observed_at
		 FROM proposals
		 WHERE repo_full_name = ?
		 ORDER BY proposal_key ASC`,
		repoFullName,
	)
	if err != nil {
		return nil, fmt.Errorf("list proposals %s: %w", repoFullName, err)
	}
	defer rows.Close()

	var out []ProposalRecord
	for rows.Next() {
		var p ProposalRecord
		var pathsJS, ifacesJS, issuesJS, upd, obs string
		if err := rows.Scan(
			&p.RepoFullName, &p.ProposalKey, &p.SourceType, &p.SourceRef, &p.SourceURL,
			&p.Title, &p.Owner, &p.State, &p.SupersededBy, &p.Version,
			&pathsJS, &ifacesJS, &issuesJS,
			&p.Summary, &p.Excerpt, &p.Visibility, &upd, &obs,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(pathsJS), &p.ScopePaths); err != nil {
			return nil, fmt.Errorf("decode proposal scope paths: %w", err)
		}
		if err := json.Unmarshal([]byte(ifacesJS), &p.Interfaces); err != nil {
			return nil, fmt.Errorf("decode proposal interfaces: %w", err)
		}
		if err := json.Unmarshal([]byte(issuesJS), &p.LinkedIssues); err != nil {
			return nil, fmt.Errorf("decode proposal issue links: %w", err)
		}
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, upd)
		p.ObservedAt, _ = time.Parse(time.RFC3339Nano, obs)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Normalize legacy optional fractional seconds before comparing UTC lease times.
const scopeExpirySQL = `substr(expires_at, 1, 19) || '.' ||
	substr(CASE WHEN substr(expires_at, 20, 1) = '.' THEN substr(rtrim(expires_at, 'Z'), 21)
		ELSE '' END || '000000000', 1, 9)`

const scopeExpiryLayout = "2006-01-02T15:04:05.000000000"

// RegisterScopeAndSnapshot atomically registers a worker's active scope and reads all
// active sibling scopes, proposals, and open PRs in one serialized transaction so
// concurrent task starts cannot both observe an empty scope set (docs/PROJECT.md §3).
func (s *SQLiteStore) RegisterScopeAndSnapshot(ctx context.Context, scope WorkerScopeRecord, now time.Time) (ScopeSnapshot, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if scope.RegisteredAt.IsZero() {
		scope.RegisteredAt = now
	}
	if scope.ExpiresAt.IsZero() {
		scope.ExpiresAt = now.Add(30 * time.Minute)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScopeSnapshot{}, fmt.Errorf("begin register scope: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Prune expired scopes for this repository inside the transaction.
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM worker_scopes WHERE repo_full_name = ? AND (`+scopeExpirySQL+`) <= ?`,
		scope.RepoFullName, now.Format(scopeExpiryLayout),
	); err != nil {
		return ScopeSnapshot{}, err
	}

	var newerGeneration bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM worker_scopes WHERE repo_full_name = ? AND worker_id = ? AND context_generation > ?)`,
		scope.RepoFullName, scope.WorkerID, scope.ContextGeneration,
	).Scan(&newerGeneration); err != nil {
		return ScopeSnapshot{}, err
	}
	if newerGeneration {
		return ScopeSnapshot{}, errors.New("worker already registered a newer context generation")
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM worker_scopes WHERE repo_full_name = ? AND worker_id = ? AND context_generation < ?`,
		scope.RepoFullName, scope.WorkerID, scope.ContextGeneration,
	); err != nil {
		return ScopeSnapshot{}, err
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO worker_scopes (
			repo_full_name, worker_id, context_generation, parent_worker_id, role,
			pr_number, base_rev, issue_links_json, paths_json, interfaces_json,
			registered_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, worker_id, context_generation) DO UPDATE SET
			parent_worker_id = excluded.parent_worker_id,
			role = excluded.role,
			pr_number = excluded.pr_number,
			base_rev = excluded.base_rev,
			issue_links_json = excluded.issue_links_json,
			paths_json = excluded.paths_json,
			interfaces_json = excluded.interfaces_json,
			registered_at = CASE WHEN
				worker_scopes.parent_worker_id != excluded.parent_worker_id OR
				worker_scopes.role != excluded.role OR
				worker_scopes.pr_number != excluded.pr_number OR
				worker_scopes.base_rev != excluded.base_rev OR
				worker_scopes.issue_links_json != excluded.issue_links_json OR
				worker_scopes.paths_json != excluded.paths_json OR
				worker_scopes.interfaces_json != excluded.interfaces_json
				THEN excluded.registered_at ELSE worker_scopes.registered_at END,
			expires_at = excluded.expires_at`,
		scope.RepoFullName, scope.WorkerID, scope.ContextGeneration,
		scope.ParentWorkerID, scope.Role, scope.PRNumber, scope.BaseRev,
		marshalJSONOrDefault(scope.IssueLinks, "[]"),
		marshalJSONOrDefault(scope.Paths, "[]"),
		marshalJSONOrDefault(scope.Interfaces, "[]"),
		scope.RegisteredAt.UTC().Format(time.RFC3339Nano),
		scope.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return ScopeSnapshot{}, fmt.Errorf("upsert worker scope: %w", err)
	}

	activeScopes, err := listActiveScopesTx(ctx, tx, scope.RepoFullName, now)
	if err != nil {
		return ScopeSnapshot{}, err
	}
	for _, active := range activeScopes {
		if active.WorkerID == scope.WorkerID && active.ContextGeneration == scope.ContextGeneration {
			scope = active
			break
		}
	}

	proposals, err := listProposalsTx(ctx, tx, scope.RepoFullName)
	if err != nil {
		return ScopeSnapshot{}, err
	}
	openPRs, err := listOpenPullRequestsTx(ctx, tx, scope.RepoFullName)
	if err != nil {
		return ScopeSnapshot{}, err
	}

	if err := tx.Commit(); err != nil {
		return ScopeSnapshot{}, err
	}
	return ScopeSnapshot{
		Scope:        scope,
		ActiveScopes: activeScopes,
		Proposals:    proposals,
		OpenPRs:      openPRs,
	}, nil
}

func (s *SQLiteStore) ListActiveScopes(ctx context.Context, repoFullName string, now time.Time) ([]WorkerScopeRecord, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return listActiveScopesTx(ctx, s.db, repoFullName, now.UTC())
}

func listActiveScopesTx(ctx context.Context, q queryable, repoFullName string, now time.Time) ([]WorkerScopeRecord, error) {
	rows, err := q.QueryContext(
		ctx,
		`SELECT repo_full_name, worker_id, context_generation, parent_worker_id, role,
		        pr_number, base_rev, issue_links_json, paths_json, interfaces_json,
		        registered_at, expires_at
		 FROM worker_scopes
		 WHERE repo_full_name = ? AND (`+scopeExpirySQL+`) > ?
		 ORDER BY worker_id ASC, context_generation ASC`,
		repoFullName, now.Format(scopeExpiryLayout),
	)
	if err != nil {
		return nil, fmt.Errorf("list active scopes %s: %w", repoFullName, err)
	}
	defer rows.Close()

	var out []WorkerScopeRecord
	for rows.Next() {
		var w WorkerScopeRecord
		var issuesJS, pathsJS, ifacesJS, regAt, expAt string
		if err := rows.Scan(
			&w.RepoFullName, &w.WorkerID, &w.ContextGeneration, &w.ParentWorkerID,
			&w.Role, &w.PRNumber, &w.BaseRev, &issuesJS, &pathsJS, &ifacesJS,
			&regAt, &expAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(issuesJS), &w.IssueLinks); err != nil {
			return nil, fmt.Errorf("decode worker scope issue links: %w", err)
		}
		if err := json.Unmarshal([]byte(pathsJS), &w.Paths); err != nil {
			return nil, fmt.Errorf("decode worker scope paths: %w", err)
		}
		if err := json.Unmarshal([]byte(ifacesJS), &w.Interfaces); err != nil {
			return nil, fmt.Errorf("decode worker scope interfaces: %w", err)
		}
		w.RegisteredAt, _ = time.Parse(time.RFC3339Nano, regAt)
		w.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expAt)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) RecordDeliveryReceipt(ctx context.Context, rec DeliveryReceipt) (DeliveryReceipt, error) {
	if rec.DeliveredAt.IsZero() {
		rec.DeliveredAt = time.Now().UTC()
	} else {
		rec.DeliveredAt = rec.DeliveredAt.UTC()
	}

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO delivery_receipts (
			repo_full_name, worker_id, context_generation, evidence_set_hash, items_json, delivered_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		rec.RepoFullName, rec.WorkerID, rec.ContextGeneration, rec.EvidenceSetHash,
		marshalJSONOrDefault(rec.Items, "[]"),
		rec.DeliveredAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return DeliveryReceipt{}, fmt.Errorf("record delivery receipt: %w", err)
	}
	rec.ID, _ = res.LastInsertId()
	return rec, nil
}

func (s *SQLiteStore) GetLatestDeliveryReceipt(ctx context.Context, repoFullName, workerID string, contextGen int) (DeliveryReceipt, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, repo_full_name, worker_id, context_generation, evidence_set_hash, items_json, delivered_at
		 FROM delivery_receipts
		 WHERE repo_full_name = ? AND worker_id = ? AND context_generation = ?
		 ORDER BY id DESC LIMIT 1`,
		repoFullName, workerID, contextGen,
	)
	rec, err := scanDeliveryReceipt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryReceipt{}, ErrNotFound
	}
	if err != nil {
		return DeliveryReceipt{}, err
	}
	return rec, nil
}

func (s *SQLiteStore) ListWorkerReceipts(ctx context.Context, repoFullName, workerID string, contextGen int) ([]DeliveryReceipt, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, repo_full_name, worker_id, context_generation, evidence_set_hash, items_json, delivered_at
		 FROM delivery_receipts
		 WHERE repo_full_name = ? AND worker_id = ? AND context_generation = ?
		 ORDER BY id DESC`,
		repoFullName, workerID, contextGen,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeliveryReceipt
	for rows.Next() {
		rec, err := scanDeliveryReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanDeliveryReceipt(s rowScanner) (DeliveryReceipt, error) {
	var rec DeliveryReceipt
	var itemsJS, delAt string
	err := s.Scan(
		&rec.ID, &rec.RepoFullName, &rec.WorkerID, &rec.ContextGeneration,
		&rec.EvidenceSetHash, &itemsJS, &delAt,
	)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := json.Unmarshal([]byte(itemsJS), &rec.Items); err != nil {
		return DeliveryReceipt{}, fmt.Errorf("decode receipt items: %w", err)
	}
	rec.DeliveredAt, _ = time.Parse(time.RFC3339Nano, delAt)
	return rec, nil
}

func (s *SQLiteStore) RecordAwarenessEvaluation(ctx context.Context, eval AwarenessEvaluation) (AwarenessEvaluation, error) {
	now := time.Now().UTC()
	if eval.IndexedAt.IsZero() {
		eval.IndexedAt = now
	}
	if eval.ValidatedAt.IsZero() {
		eval.ValidatedAt = now
	}
	if eval.RevisionKind == "" {
		eval.RevisionKind = "pr_head"
	}

	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO awareness_evaluations (
			repo_full_name, pr_number, head_sha, revision_kind, worker_id,
			context_generation, evidence_set_hash, verdict, complete, reason,
			check_run_id, indexed_at, validated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eval.RepoFullName, eval.PRNumber, eval.HeadSHA, eval.RevisionKind, eval.WorkerID,
		eval.ContextGeneration, eval.EvidenceSetHash, eval.Verdict, boolToInt(eval.Complete),
		eval.Reason, eval.CheckRunID,
		eval.IndexedAt.UTC().Format(time.RFC3339Nano),
		eval.ValidatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return AwarenessEvaluation{}, fmt.Errorf("record awareness evaluation: %w", err)
	}
	eval.ID, _ = res.LastInsertId()
	return eval, nil
}

func (s *SQLiteStore) GetLatestAwarenessEvaluation(ctx context.Context, repoFullName string, prNumber int, headSHA string) (AwarenessEvaluation, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, repo_full_name, pr_number, head_sha, revision_kind, worker_id,
		        context_generation, evidence_set_hash, verdict, complete, reason,
		        check_run_id, indexed_at, validated_at
		 FROM awareness_evaluations
		 WHERE repo_full_name = ? AND pr_number = ? AND head_sha = ? AND revision_kind = 'pr_head'
		 ORDER BY id DESC LIMIT 1`,
		repoFullName, prNumber, headSHA,
	)
	var eval AwarenessEvaluation
	var complete int
	var idxAt, valAt string
	err := row.Scan(
		&eval.ID, &eval.RepoFullName, &eval.PRNumber, &eval.HeadSHA, &eval.RevisionKind,
		&eval.WorkerID, &eval.ContextGeneration, &eval.EvidenceSetHash, &eval.Verdict,
		&complete, &eval.Reason, &eval.CheckRunID, &idxAt, &valAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AwarenessEvaluation{}, ErrNotFound
	}
	if err != nil {
		return AwarenessEvaluation{}, err
	}
	eval.Complete = complete != 0
	eval.IndexedAt, _ = time.Parse(time.RFC3339Nano, idxAt)
	eval.ValidatedAt, _ = time.Parse(time.RFC3339Nano, valAt)
	return eval, nil
}

func (s *SQLiteStore) UpsertOverlapAcknowledgment(ctx context.Context, ack OverlapAcknowledgment) error {
	a, b := ack.PRA, ack.PRB
	if a > b {
		a, b = b, a
	}
	if ack.AcknowledgedAt.IsZero() {
		ack.AcknowledgedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO overlap_acknowledgments (
			repo_full_name, pr_a, pr_b, disposition, actor, note, acknowledged_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, pr_a, pr_b) DO UPDATE SET
			disposition = excluded.disposition,
			actor = excluded.actor,
			note = excluded.note,
			acknowledged_at = excluded.acknowledged_at`,
		ack.RepoFullName, a, b, ack.Disposition, ack.Actor, ack.Note,
		ack.AcknowledgedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert overlap acknowledgment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListOverlapAcknowledgments(ctx context.Context, repoFullName string) ([]OverlapAcknowledgment, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT repo_full_name, pr_a, pr_b, disposition, actor, note, acknowledged_at
		 FROM overlap_acknowledgments
		 WHERE repo_full_name = ?
		 ORDER BY pr_a ASC, pr_b ASC`,
		repoFullName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OverlapAcknowledgment
	for rows.Next() {
		var ack OverlapAcknowledgment
		var ackAt string
		if err := rows.Scan(
			&ack.RepoFullName, &ack.PRA, &ack.PRB, &ack.Disposition,
			&ack.Actor, &ack.Note, &ackAt,
		); err != nil {
			return nil, err
		}
		ack.AcknowledgedAt, _ = time.Parse(time.RFC3339Nano, ackAt)
		out = append(out, ack)
	}
	return out, rows.Err()
}

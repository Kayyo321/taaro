package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PublishArtifact writes a new immutable version of (repo_full_name, artifact_key)
// and returns the persisted artifact along with all subscribers now behind the new version.
func (s *SQLiteStore) PublishArtifact(ctx context.Context, rec ArtifactRecord) (ArtifactRecord, []ArtifactSubscription, error) {
	createdAt := rec.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if rec.ContentType == "" {
		rec.ContentType = "text/plain"
	}
	sum := sha256.Sum256([]byte(rec.Payload))
	rec.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	rec.CreatedAt = createdAt

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactRecord{}, nil, fmt.Errorf("begin publish artifact: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxVer int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version), 0) FROM artifacts WHERE repo_full_name = ? AND artifact_key = ?`,
		rec.RepoFullName, rec.ArtifactKey,
	).Scan(&maxVer); err != nil {
		return ArtifactRecord{}, nil, fmt.Errorf("query max artifact version: %w", err)
	}
	rec.Version = maxVer + 1

	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO artifacts (
			repo_full_name, artifact_key, version, producer_worker, producer_rev,
			pr_number, command, content_type, payload, checksum, summary, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RepoFullName, rec.ArtifactKey, rec.Version, rec.ProducerWorker, rec.ProducerRev,
		rec.PRNumber, rec.Command, rec.ContentType, rec.Payload, rec.Checksum, rec.Summary,
		createdAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return ArtifactRecord{}, nil, fmt.Errorf("insert artifact %s/%s@v%d: %w", rec.RepoFullName, rec.ArtifactKey, rec.Version, err)
	}
	rec.ID, _ = res.LastInsertId()

	stale, err := listArtifactConsumersTx(ctx, tx, rec.RepoFullName, rec.ArtifactKey, rec.Version)
	if err != nil {
		return ArtifactRecord{}, nil, err
	}
	var impacted []ArtifactSubscription
	for _, sub := range stale {
		if sub.Stale {
			impacted = append(impacted, sub)
		}
	}

	if err := tx.Commit(); err != nil {
		return ArtifactRecord{}, nil, err
	}
	return rec, impacted, nil
}

// GetArtifact fetches a specific immutable version (when version > 0) or the latest version (version == 0).
func (s *SQLiteStore) GetArtifact(ctx context.Context, repoFullName, artifactKey string, version int) (ArtifactRecord, error) {
	var row *sql.Row
	if version > 0 {
		row = s.db.QueryRowContext(
			ctx,
			`SELECT id, repo_full_name, artifact_key, version, producer_worker, producer_rev,
			        pr_number, command, content_type, payload, checksum, summary, created_at
			 FROM artifacts
			 WHERE repo_full_name = ? AND artifact_key = ? AND version = ?`,
			repoFullName, artifactKey, version,
		)
	} else {
		row = s.db.QueryRowContext(
			ctx,
			`SELECT id, repo_full_name, artifact_key, version, producer_worker, producer_rev,
			        pr_number, command, content_type, payload, checksum, summary, created_at
			 FROM artifacts
			 WHERE repo_full_name = ? AND artifact_key = ?
			 ORDER BY version DESC LIMIT 1`,
			repoFullName, artifactKey,
		)
	}

	rec, err := scanArtifact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactRecord{}, ErrNotFound
	}
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("get artifact %s/%s: %w", repoFullName, artifactKey, err)
	}
	return rec, nil
}

// ListLatestArtifacts returns the highest immutable version for every artifact key in a repository.
func (s *SQLiteStore) ListLatestArtifacts(ctx context.Context, repoFullName string) ([]ArtifactRecord, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT a.id, a.repo_full_name, a.artifact_key, a.version, a.producer_worker, a.producer_rev,
		        a.pr_number, a.command, a.content_type, a.payload, a.checksum, a.summary, a.created_at
		 FROM artifacts a
		 INNER JOIN (
			SELECT repo_full_name, artifact_key, MAX(version) AS max_ver
			FROM artifacts
			WHERE repo_full_name = ?
			GROUP BY repo_full_name, artifact_key
		 ) latest
		 ON a.repo_full_name = latest.repo_full_name
		 AND a.artifact_key = latest.artifact_key
		 AND a.version = latest.max_ver
		 ORDER BY a.artifact_key ASC`,
		repoFullName,
	)
	if err != nil {
		return nil, fmt.Errorf("list latest artifacts %s: %w", repoFullName, err)
	}
	defer rows.Close()

	var out []ArtifactRecord
	for rows.Next() {
		rec, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanArtifact(s rowScanner) (ArtifactRecord, error) {
	var rec ArtifactRecord
	var createdAt string
	err := s.Scan(
		&rec.ID, &rec.RepoFullName, &rec.ArtifactKey, &rec.Version,
		&rec.ProducerWorker, &rec.ProducerRev, &rec.PRNumber,
		&rec.Command, &rec.ContentType, &rec.Payload, &rec.Checksum,
		&rec.Summary, &createdAt,
	)
	if err != nil {
		return ArtifactRecord{}, err
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return rec, nil
}

// UpsertArtifactSubscription registers or updates a worker's subscription and consumed version for an artifact key.
func (s *SQLiteStore) UpsertArtifactSubscription(ctx context.Context, sub ArtifactSubscription) (ArtifactSubscription, error) {
	if sub.ConsumedVersion < 0 || (sub.ConsumedVersion > 0 && strings.TrimSpace(sub.ConsumerRev) == "") {
		return ArtifactSubscription{}, fmt.Errorf("%w: consumption requires an existing version and consumer_rev", ErrInvalidSubscription)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	consumedAt := ""
	if sub.ConsumedVersion > 0 {
		consumedAt = now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactSubscription{}, err
	}
	defer tx.Rollback()
	var latest int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM artifacts WHERE repo_full_name=? AND artifact_key=?`, sub.RepoFullName, sub.ArtifactKey).Scan(&latest); err != nil {
		return ArtifactSubscription{}, err
	}
	if sub.ConsumedVersion > latest {
		return ArtifactSubscription{}, fmt.Errorf("%w: version %d has not been published", ErrInvalidSubscription, sub.ConsumedVersion)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO artifact_subscriptions (
			repo_full_name, artifact_key, worker_id, context_generation,
			consumed_version, consumer_rev, subscribed_at, consumed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_full_name, artifact_key, worker_id, context_generation) DO UPDATE SET
			consumed_version = CASE WHEN excluded.consumed_version > 0 THEN excluded.consumed_version ELSE artifact_subscriptions.consumed_version END,
			consumer_rev = CASE WHEN excluded.consumed_version > 0 THEN excluded.consumer_rev ELSE artifact_subscriptions.consumer_rev END,
			consumed_at = CASE WHEN excluded.consumed_at != '' THEN excluded.consumed_at ELSE artifact_subscriptions.consumed_at END`,
		sub.RepoFullName, sub.ArtifactKey, sub.WorkerID, sub.ContextGeneration,
		sub.ConsumedVersion, sub.ConsumerRev, now, consumedAt,
	)
	if err != nil {
		return ArtifactSubscription{}, fmt.Errorf("upsert artifact subscription: %w", err)
	}

	var subscribed, consumed string
	if err := tx.QueryRowContext(ctx, `SELECT consumed_version,consumer_rev,subscribed_at,consumed_at FROM artifact_subscriptions
 WHERE repo_full_name=? AND artifact_key=? AND worker_id=? AND context_generation=?`, sub.RepoFullName, sub.ArtifactKey, sub.WorkerID, sub.ContextGeneration).Scan(&sub.ConsumedVersion, &sub.ConsumerRev, &subscribed, &consumed); err != nil {
		return ArtifactSubscription{}, err
	}
	sub.SubscribedAt, _ = time.Parse(time.RFC3339Nano, subscribed)
	sub.ConsumedAt, _ = time.Parse(time.RFC3339Nano, consumed)
	sub.LatestVersion = latest
	sub.Stale = sub.ConsumedVersion < latest
	return sub, tx.Commit()
}

var ErrInvalidSubscription = errors.New("invalid artifact subscription")

func (s *SQLiteStore) ListArtifactConsumers(ctx context.Context, repoFullName, artifactKey string) ([]ArtifactSubscription, error) {
	var latest int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version), 0) FROM artifacts WHERE repo_full_name = ? AND artifact_key = ?`,
		repoFullName, artifactKey,
	).Scan(&latest); err != nil {
		return nil, err
	}
	return listArtifactConsumersTx(ctx, s.db, repoFullName, artifactKey, latest)
}

func listArtifactConsumersTx(ctx context.Context, q queryable, repoFullName, artifactKey string, latest int) ([]ArtifactSubscription, error) {
	rows, err := q.QueryContext(
		ctx,
		`SELECT repo_full_name, artifact_key, worker_id, context_generation,
		        consumed_version, consumer_rev, subscribed_at, consumed_at
		 FROM artifact_subscriptions
		 WHERE repo_full_name = ? AND artifact_key = ?
		 ORDER BY worker_id ASC, context_generation ASC`,
		repoFullName, artifactKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifact consumers: %w", err)
	}
	defer rows.Close()

	var out []ArtifactSubscription
	for rows.Next() {
		var sub ArtifactSubscription
		var subAt, consAt string
		if err := rows.Scan(
			&sub.RepoFullName, &sub.ArtifactKey, &sub.WorkerID, &sub.ContextGeneration,
			&sub.ConsumedVersion, &sub.ConsumerRev, &subAt, &consAt,
		); err != nil {
			return nil, err
		}
		sub.SubscribedAt, _ = time.Parse(time.RFC3339Nano, subAt)
		if consAt != "" {
			sub.ConsumedAt, _ = time.Parse(time.RFC3339Nano, consAt)
		}
		sub.LatestVersion = latest
		sub.Stale = latest > 0 && sub.ConsumedVersion < latest
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListWorkerArtifactSubscriptions(ctx context.Context, repoFullName, workerID string, contextGen int) ([]ArtifactSubscription, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT s.repo_full_name, s.artifact_key, s.worker_id, s.context_generation,
		        s.consumed_version, s.consumer_rev, s.subscribed_at, s.consumed_at,
		        COALESCE((SELECT MAX(a.version) FROM artifacts a WHERE a.repo_full_name = s.repo_full_name AND a.artifact_key = s.artifact_key), 0) AS latest_ver
		 FROM artifact_subscriptions s
		 WHERE s.repo_full_name = ? AND s.worker_id = ? AND s.context_generation = ?
		 ORDER BY s.artifact_key ASC`,
		repoFullName, workerID, contextGen,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker artifact subscriptions: %w", err)
	}
	defer rows.Close()

	var out []ArtifactSubscription
	for rows.Next() {
		var sub ArtifactSubscription
		var subAt, consAt string
		if err := rows.Scan(
			&sub.RepoFullName, &sub.ArtifactKey, &sub.WorkerID, &sub.ContextGeneration,
			&sub.ConsumedVersion, &sub.ConsumerRev, &subAt, &consAt, &sub.LatestVersion,
		); err != nil {
			return nil, err
		}
		sub.SubscribedAt, _ = time.Parse(time.RFC3339Nano, subAt)
		if consAt != "" {
			sub.ConsumedAt, _ = time.Parse(time.RFC3339Nano, consAt)
		}
		sub.Stale = sub.LatestVersion > 0 && sub.ConsumedVersion < sub.LatestVersion
		out = append(out, sub)
	}
	return out, rows.Err()
}

// RecordValidationCheck inserts a validation check record and automatically cancels any older
// in-flight speculative runs for the same (repo, job_name, pr_number) on superseded revision pairs.
func (s *SQLiteStore) RecordValidationCheck(ctx context.Context, rec ValidationCheckRecord) (ValidationCheckRecord, []int64, error) {
	now := time.Now().UTC()
	if rec.StartedAt.IsZero() {
		rec.StartedAt = now
	}
	if rec.Status == "" {
		rec.Status = "completed"
	}
	var completedStr string
	if rec.Status == "completed" {
		if rec.CompletedAt.IsZero() {
			rec.CompletedAt = now
		}
		completedStr = rec.CompletedAt.UTC().Format(time.RFC3339Nano)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ValidationCheckRecord{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO validation_checks (
			repo_full_name, job_name, pr_number, producer_rev, consumer_rev,
			inputs_hash, toolchain, flags, environment, artifact_key, artifact_version,
			status, conclusion, flaky, output_summary, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RepoFullName, rec.JobName, rec.PRNumber, rec.ProducerRev, rec.ConsumerRev,
		rec.InputsHash, rec.Toolchain, rec.Flags, rec.Environment,
		rec.ArtifactKey, rec.ArtifactVersion, rec.Status, rec.Conclusion,
		boolToInt(rec.Flaky), rec.OutputSummary,
		rec.StartedAt.UTC().Format(time.RFC3339Nano), completedStr,
	)
	if err != nil {
		return ValidationCheckRecord{}, nil, fmt.Errorf("insert validation check: %w", err)
	}
	rec.ID, _ = res.LastInsertId()

	// Cancel older queued/in_progress speculative checks for the same job & PR with different revision pairs.
	var cancelledIDs []int64
	if rec.PRNumber > 0 {
		rows, err := tx.QueryContext(
			ctx,
			`SELECT id FROM validation_checks
			 WHERE repo_full_name = ? AND job_name = ? AND pr_number = ?
			   AND id != ? AND status IN ('queued', 'in_progress')
			   AND (producer_rev != ? OR consumer_rev != ?)`,
			rec.RepoFullName, rec.JobName, rec.PRNumber, rec.ID, rec.ProducerRev, rec.ConsumerRev,
		)
		if err != nil {
			return ValidationCheckRecord{}, nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return ValidationCheckRecord{}, nil, err
			}
			cancelledIDs = append(cancelledIDs, id)
		}
		if err := rows.Close(); err != nil {
			return ValidationCheckRecord{}, nil, err
		}

		for _, oldID := range cancelledIDs {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE validation_checks
				 SET status = 'completed', conclusion = 'cancelled', superseded_by_id = ?, completed_at = ?
				 WHERE id = ?`,
				rec.ID, now.Format(time.RFC3339Nano), oldID,
			); err != nil {
				return ValidationCheckRecord{}, nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return ValidationCheckRecord{}, nil, err
	}
	return rec, cancelledIDs, nil
}

func (s *SQLiteStore) ListValidationChecks(ctx context.Context, repoFullName string, prNumber int) ([]ValidationCheckRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if prNumber > 0 {
		rows, err = s.db.QueryContext(
			ctx,
			`SELECT id, repo_full_name, job_name, pr_number, producer_rev, consumer_rev,
			        inputs_hash, toolchain, flags, environment, artifact_key, artifact_version,
			        status, conclusion, flaky, output_summary, superseded_by_id, started_at, completed_at
			 FROM validation_checks
			 WHERE repo_full_name = ? AND pr_number = ?
			 ORDER BY id DESC`,
			repoFullName, prNumber,
		)
	} else {
		rows, err = s.db.QueryContext(
			ctx,
			`SELECT id, repo_full_name, job_name, pr_number, producer_rev, consumer_rev,
			        inputs_hash, toolchain, flags, environment, artifact_key, artifact_version,
			        status, conclusion, flaky, output_summary, superseded_by_id, started_at, completed_at
			 FROM validation_checks
			 WHERE repo_full_name = ?
			 ORDER BY id DESC LIMIT 100`,
			repoFullName,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list validation checks: %w", err)
	}
	defer rows.Close()

	var out []ValidationCheckRecord
	for rows.Next() {
		rec, err := scanValidationCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// FindEligibleCheck finds the newest non-flaky, non-cancelled check matching all execution identity fields.
func (s *SQLiteStore) FindEligibleCheck(ctx context.Context, repoFullName, jobName, producerRev, consumerRev, inputsHash, toolchain, flags, environment string) (ValidationCheckRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, repo_full_name, job_name, pr_number, producer_rev, consumer_rev,
		        inputs_hash, toolchain, flags, environment, artifact_key, artifact_version,
		        status, conclusion, flaky, output_summary, superseded_by_id, started_at, completed_at
		 FROM validation_checks
		 WHERE repo_full_name = ? AND job_name = ? AND producer_rev = ? AND consumer_rev = ?
		   AND inputs_hash = ? AND toolchain = ? AND flags = ? AND environment = ?
		
		 ORDER BY id DESC LIMIT 1`,
		repoFullName, jobName, producerRev, consumerRev, inputsHash, toolchain, flags, environment,
	)
	rec, err := scanValidationCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidationCheckRecord{}, ErrNotFound
	}
	if err != nil {
		return ValidationCheckRecord{}, err
	}
	if rec.Flaky || rec.SupersededByID != 0 {
		return ValidationCheckRecord{}, ErrNotFound
	}
	switch rec.Status {
	case "queued", "in_progress":
	case "completed":
		if rec.Conclusion != "failure" && rec.Conclusion != "success" {
			return ValidationCheckRecord{}, ErrNotFound
		}
	default:
		return ValidationCheckRecord{}, ErrNotFound
	}
	return rec, nil
}

func scanValidationCheck(s rowScanner) (ValidationCheckRecord, error) {
	var rec ValidationCheckRecord
	var flaky int
	var started, completed string
	err := s.Scan(
		&rec.ID, &rec.RepoFullName, &rec.JobName, &rec.PRNumber,
		&rec.ProducerRev, &rec.ConsumerRev, &rec.InputsHash,
		&rec.Toolchain, &rec.Flags, &rec.Environment,
		&rec.ArtifactKey, &rec.ArtifactVersion, &rec.Status, &rec.Conclusion,
		&flaky, &rec.OutputSummary, &rec.SupersededByID, &started, &completed,
	)
	if err != nil {
		return ValidationCheckRecord{}, err
	}
	rec.Flaky = flaky != 0
	rec.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if completed != "" {
		rec.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed)
	}
	return rec, nil
}

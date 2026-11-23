package store

import (
	"context"
	"fmt"
)

var repositoryTables = []string{
	"webhook_deliveries", "installation_repositories", "pull_requests", "pull_request_revisions",
	"pull_request_reviews", "external_check_runs", "artifacts", "artifact_subscriptions", "validation_checks",
	"proposals", "worker_scopes", "delivery_receipts", "awareness_evaluations", "overlap_acknowledgments",
	"handoff_refreshes", "proposal_awareness_refresh", "awareness_publication_watermarks",
}

// ValidateRepositoryScope prevents running with another repository's existing data or queued work.
func (s *SQLiteStore) ValidateRepositoryScope(ctx context.Context, repo string) error {
	if repo == "" {
		return nil
	}
	for _, table := range repositoryTables {
		var outside bool
		err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE repo_full_name <> '' AND repo_full_name <> ?)", repo).Scan(&outside)
		if err != nil {
			return fmt.Errorf("verify repository scope in %s: %w", table, err)
		}
		if outside {
			return fmt.Errorf("%s contains data outside the configured repository; use a fresh database or scoped backup", table)
		}
	}
	return nil
}

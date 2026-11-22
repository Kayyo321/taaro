package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// DeliveryRecord records a GitHub webhook delivery for deduplication and auditability.
type DeliveryRecord struct {
	DeliveryID   string
	Event        string
	Action       string
	RepoFullName string
	ReceivedAt   time.Time
}

// PullRequestRecord stores the latest observed pull request state pinned to its head SHA.
type PullRequestRecord struct {
	RepoFullName   string    `json:"repo"`
	Number         int       `json:"pr_number"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	Draft          bool      `json:"draft"`
	HeadSHA        string    `json:"head_sha"`
	BaseRef        string    `json:"base_ref"`
	HTMLURL        string    `json:"html_url"`
	InstallationID int64     `json:"installation_id"`
	CheckRunID     int64     `json:"check_run_id,omitempty"`
	CheckRunHead   string    `json:"check_run_head_sha,omitempty"`
	LastAction     string    `json:"last_action"`
	ObservedAt     time.Time `json:"observed_at"`
}

// Store persists webhook deduplication records and observed repository context.
type Store interface {
	// RecordDelivery atomically inserts a delivery ID. It returns (true, nil) if this
	// delivery was newly recorded, or (false, nil) if it was already processed.
	RecordDelivery(ctx context.Context, rec DeliveryRecord) (bool, error)

	// UpsertPullRequest stores or updates the latest observed state for a PR.
	UpsertPullRequest(ctx context.Context, rec PullRequestRecord) error

	// RecordHandoffCheckRun links a published Check Run ID to a PR head SHA.
	RecordHandoffCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64) error

	// GetPullRequest fetches the latest observed state for a PR, or ErrNotFound.
	GetPullRequest(ctx context.Context, repoFullName string, prNumber int) (PullRequestRecord, error)

	Close() error
}

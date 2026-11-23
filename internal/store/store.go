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
	InstallationID int64
	DeliveryID     string
	Event          string
	Action         string
	RepoFullName   string
	ReceivedAt     time.Time
	Payload        []byte
}

// PullRequestRecord stores the latest observed pull request state pinned to its head SHA.
type PullRequestRecord struct {
	RepoFullName        string    `json:"repo"`
	Number              int       `json:"pr_number"`
	Title               string    `json:"title"`
	State               string    `json:"state"`
	Draft               bool      `json:"draft"`
	HeadSHA             string    `json:"head_sha"`
	BaseRef             string    `json:"base_ref"`
	HTMLURL             string    `json:"html_url"`
	Author              string    `json:"author,omitempty"`
	Body                string    `json:"body,omitempty"`
	Labels              []string  `json:"labels,omitempty"`
	LinkedIssues        []int     `json:"linked_issues,omitempty"`
	Paths               []string  `json:"paths,omitempty"`
	Merged              bool      `json:"merged,omitempty"`
	MergeCommitSHA      string    `json:"merge_commit_sha,omitempty"`
	RevisionKind        string    `json:"revision_kind,omitempty"`
	InstallationID      int64     `json:"installation_id"`
	CheckRunID          int64     `json:"check_run_id,omitempty"`
	CheckRunHead        string    `json:"check_run_head_sha,omitempty"`
	AwarenessCheckRunID int64     `json:"awareness_check_run_id,omitempty"`
	AwarenessCheckHead  string    `json:"awareness_check_head_sha,omitempty"`
	AwarenessConclusion string    `json:"awareness_conclusion,omitempty"`
	LastAction          string    `json:"last_action"`
	ObservedAt          time.Time `json:"observed_at"`
}

// PullRequestRevision records a revision-anchored snapshot for a specific PR head SHA.
type PullRequestRevision struct {
	RepoFullName        string    `json:"repo"`
	PRNumber            int       `json:"pr_number"`
	HeadSHA             string    `json:"head_sha"`
	BaseRef             string    `json:"base_ref"`
	Action              string    `json:"action"`
	RevisionKind        string    `json:"revision_kind"`
	CheckRunID          int64     `json:"check_run_id,omitempty"`
	AwarenessCheckRunID int64     `json:"awareness_check_run_id,omitempty"`
	AwarenessConclusion string    `json:"awareness_conclusion,omitempty"`
	SupersededBySHA     string    `json:"superseded_by_sha,omitempty"`
	ObservedAt          time.Time `json:"observed_at"`
}

// ReviewRecord stores an observed pull request review anchored to the commit it reviewed.
type ReviewRecord struct {
	RepoFullName string    `json:"repo"`
	PRNumber     int       `json:"pr_number"`
	ReviewID     int64     `json:"review_id"`
	Reviewer     string    `json:"reviewer"`
	State        string    `json:"state"`
	CommitSHA    string    `json:"commit_sha"`
	BodyExcerpt  string    `json:"body_excerpt,omitempty"`
	HTMLURL      string    `json:"html_url,omitempty"`
	SubmittedAt  time.Time `json:"submitted_at"`
	ObservedAt   time.Time `json:"observed_at"`
}

// ExternalCheckRun stores a non-Taaro GitHub check run observed for a commit SHA.
type ExternalCheckRun struct {
	RepoFullName string    `json:"repo"`
	HeadSHA      string    `json:"head_sha"`
	CheckRunID   int64     `json:"check_run_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion,omitempty"`
	DetailsURL   string    `json:"details_url,omitempty"`
	AppID        int64     `json:"app_id,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
}

// ArtifactRecord represents an immutable, task-scoped artifact (e.g. a golden diagnostic fixture).
type ArtifactRecord struct {
	ID             int64     `json:"id"`
	RepoFullName   string    `json:"repo"`
	ArtifactKey    string    `json:"artifact_key"`
	Version        int       `json:"version"`
	ProducerWorker string    `json:"producer_worker"`
	ProducerRev    string    `json:"producer_rev"`
	PRNumber       int       `json:"pr_number,omitempty"`
	Command        string    `json:"command,omitempty"`
	ContentType    string    `json:"content_type"`
	Payload        string    `json:"payload"`
	Checksum       string    `json:"checksum"`
	Summary        string    `json:"summary,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ArtifactSubscription tracks which worker and context generation subscribed to or consumed an artifact version.
type ArtifactSubscription struct {
	RepoFullName      string    `json:"repo"`
	ArtifactKey       string    `json:"artifact_key"`
	WorkerID          string    `json:"worker_id"`
	ContextGeneration int       `json:"context_generation"`
	ConsumedVersion   int       `json:"consumed_version"`
	LatestVersion     int       `json:"latest_version,omitempty"`
	Stale             bool      `json:"stale,omitempty"`
	ConsumerRev       string    `json:"consumer_rev,omitempty"`
	SubscribedAt      time.Time `json:"subscribed_at"`
	ConsumedAt        time.Time `json:"consumed_at,omitempty"`
}

// ValidationCheckRecord stores an observed or combined compatibility check run with full execution identity.
type ValidationCheckRecord struct {
	ID              int64     `json:"id"`
	RepoFullName    string    `json:"repo"`
	JobName         string    `json:"job_name"`
	PRNumber        int       `json:"pr_number,omitempty"`
	ProducerRev     string    `json:"producer_rev"`
	ConsumerRev     string    `json:"consumer_rev"`
	InputsHash      string    `json:"inputs_hash"`
	Toolchain       string    `json:"toolchain"`
	Flags           string    `json:"flags"`
	Environment     string    `json:"environment"`
	ArtifactKey     string    `json:"artifact_key,omitempty"`
	ArtifactVersion int       `json:"artifact_version,omitempty"`
	Status          string    `json:"status"`
	Conclusion      string    `json:"conclusion,omitempty"`
	Flaky           bool      `json:"flaky,omitempty"`
	OutputSummary   string    `json:"output_summary,omitempty"`
	SupersededByID  int64     `json:"superseded_by_id,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

// ProposalRecord stores a designated design PR, labeled issue, or versioned RFC proposal.
type ProposalRecord struct {
	RepoFullName string    `json:"repo"`
	ProposalKey  string    `json:"proposal_key"`
	SourceType   string    `json:"source_type"` // "issue", "pr", "rfc"
	SourceRef    string    `json:"source_ref"`
	SourceURL    string    `json:"source_url,omitempty"`
	Title        string    `json:"title"`
	Owner        string    `json:"owner,omitempty"`
	State        string    `json:"state"` // "draft", "accepted", "withdrawn", "superseded"
	SupersededBy string    `json:"superseded_by,omitempty"`
	Version      int       `json:"version"`
	ScopePaths   []string  `json:"scope_paths,omitempty"`
	Interfaces   []string  `json:"interfaces,omitempty"`
	LinkedIssues []int     `json:"linked_issues,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Excerpt      string    `json:"excerpt,omitempty"`
	Visibility   string    `json:"visibility"` // "public", "private"
	UpdatedAt    time.Time `json:"updated_at"`
	ObservedAt   time.Time `json:"observed_at"`
}

// WorkerScopeRecord tracks an active worker's declared scope for atomic overlap detection.
type WorkerScopeRecord struct {
	RepoFullName      string    `json:"repo"`
	WorkerID          string    `json:"worker_id"`
	ContextGeneration int       `json:"context_generation"`
	ParentWorkerID    string    `json:"parent_worker_id,omitempty"`
	Role              string    `json:"role,omitempty"`
	PRNumber          int       `json:"pr_number,omitempty"`
	BaseRev           string    `json:"base_rev,omitempty"`
	IssueLinks        []int     `json:"issue_links,omitempty"`
	Paths             []string  `json:"paths,omitempty"`
	Interfaces        []string  `json:"interfaces,omitempty"`
	RegisteredAt      time.Time `json:"registered_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// ScopeSnapshot captures the consistent state observed atomically when registering a worker scope.
type ScopeSnapshot struct {
	Scope        WorkerScopeRecord
	ActiveScopes []WorkerScopeRecord
	Proposals    []ProposalRecord
	OpenPRs      []PullRequestRecord
}

// ReceiptItem identifies a specific versioned piece of evidence delivered to a worker.
type ReceiptItem struct {
	Kind    string `json:"kind"` // "proposal", "scope", "pr", "artifact"
	Key     string `json:"key"`
	Version string `json:"version"`
	State   string `json:"state,omitempty"`
}

// DeliveryReceipt records verified delivery of an exact evidence set to (worker_id, context_generation).
type DeliveryReceipt struct {
	ID                int64         `json:"id"`
	RepoFullName      string        `json:"repo"`
	WorkerID          string        `json:"worker_id"`
	ContextGeneration int           `json:"context_generation"`
	EvidenceSetHash   string        `json:"evidence_set_hash"`
	Items             []ReceiptItem `json:"items"`
	DeliveredAt       time.Time     `json:"delivered_at"`
}

// AwarenessEvaluation records the outcome of evaluating a candidate revision for required awareness.
type AwarenessEvaluation struct {
	ID                int64     `json:"id"`
	RepoFullName      string    `json:"repo"`
	PRNumber          int       `json:"pr_number,omitempty"`
	HeadSHA           string    `json:"head_sha"`
	RevisionKind      string    `json:"revision_kind"` // "pr_head", "merge_group"
	WorkerID          string    `json:"worker_id,omitempty"`
	ContextGeneration int       `json:"context_generation,omitempty"`
	EvidenceSetHash   string    `json:"evidence_set_hash"`
	Verdict           string    `json:"verdict"` // "success", "action_required", "in_progress", "failure"
	Complete          bool      `json:"complete"`
	Reason            string    `json:"reason,omitempty"`
	ProposalWatermark int64 `json:"-"`
	CheckRunID        int64     `json:"check_run_id,omitempty"`
	IndexedAt         time.Time `json:"indexed_at"`
	ValidatedAt       time.Time `json:"validated_at"`
}

// OverlapAcknowledgment records an author's disposition on a pair of overlapping PRs.
type OverlapAcknowledgment struct {
	RepoFullName   string    `json:"repo"`
	PRA            int       `json:"pr_a"`
	PRB            int       `json:"pr_b"`
	Disposition    string    `json:"disposition"` // "complementary", "combined", "dismissed"
	Actor          string    `json:"actor,omitempty"`
	Note           string    `json:"note,omitempty"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

// Store persists webhook deduplication records, observed repository context,
// immutable artifacts, proposals, active scopes, receipts, and awareness evaluations.
type Store interface {
	// RecordDelivery atomically inserts a delivery ID. It returns (true, nil) if this
	// delivery was newly recorded, or (false, nil) if it was already processed.
	RecordDelivery(ctx context.Context, rec DeliveryRecord) (bool, error)

	NextDelivery(context.Context, time.Time) (DeliveryRecord, error)
	FinishDelivery(context.Context, string, bool) error

	// Pull requests, revisions, reviews, and external check runs.
	UpsertPullRequest(ctx context.Context, rec PullRequestRecord) error
	RecordHandoffCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64) error
	RecordAwarenessCheckRun(ctx context.Context, repoFullName string, prNumber int, headSHA string, checkRunID int64, conclusion string) error
	GetPullRequest(ctx context.Context, repoFullName string, prNumber int) (PullRequestRecord, error)
	ListOpenPullRequests(ctx context.Context, repoFullName string) ([]PullRequestRecord, error)
	ListPullRequestRevisions(ctx context.Context, repoFullName string, prNumber int) ([]PullRequestRevision, error)
	UpsertReview(ctx context.Context, rec ReviewRecord) error
	ListReviews(ctx context.Context, repoFullName string, prNumber int) ([]ReviewRecord, error)
	UpsertExternalCheckRun(ctx context.Context, rec ExternalCheckRun) error
	ListExternalCheckRuns(ctx context.Context, repoFullName, headSHA string) ([]ExternalCheckRun, error)

	// Immutable artifacts and subscriptions.
	PublishArtifact(ctx context.Context, rec ArtifactRecord) (ArtifactRecord, []ArtifactSubscription, error)
	GetArtifact(ctx context.Context, repoFullName, artifactKey string, version int) (ArtifactRecord, error)
	ListLatestArtifacts(ctx context.Context, repoFullName string) ([]ArtifactRecord, error)
	UpsertArtifactSubscription(ctx context.Context, sub ArtifactSubscription) (ArtifactSubscription, error)
	ListArtifactConsumers(ctx context.Context, repoFullName, artifactKey string) ([]ArtifactSubscription, error)
	ListWorkerArtifactSubscriptions(ctx context.Context, repoFullName, workerID string, contextGen int) ([]ArtifactSubscription, error)

	// Validation & combined compatibility checks.
	RecordValidationCheck(ctx context.Context, rec ValidationCheckRecord) (ValidationCheckRecord, []int64, error)
	ListValidationChecks(ctx context.Context, repoFullName string, prNumber int) ([]ValidationCheckRecord, error)
	FindEligibleCheck(ctx context.Context, repoFullName, jobName, producerRev, consumerRev, inputsHash, toolchain, flags, environment string) (ValidationCheckRecord, error)

	// Proposals, active scopes, receipts, awareness evaluations, and overlaps.
	UpsertProposal(ctx context.Context, rec ProposalRecord) (ProposalRecord, error)
	ListProposals(ctx context.Context, repoFullName string) ([]ProposalRecord, error)
	RegisterScopeAndSnapshot(ctx context.Context, scope WorkerScopeRecord, now time.Time) (ScopeSnapshot, error)
	ListActiveScopes(ctx context.Context, repoFullName string, now time.Time) ([]WorkerScopeRecord, error)
	RecordDeliveryReceipt(ctx context.Context, rec DeliveryReceipt) (DeliveryReceipt, error)
	GetLatestDeliveryReceipt(ctx context.Context, repoFullName, workerID string, contextGen int) (DeliveryReceipt, error)
	ListWorkerReceipts(ctx context.Context, repoFullName, workerID string, contextGen int) ([]DeliveryReceipt, error)
	RecordAwarenessEvaluation(ctx context.Context, eval AwarenessEvaluation) (AwarenessEvaluation, error)
	GetLatestAwarenessEvaluation(ctx context.Context, repoFullName string, prNumber int, headSHA string) (AwarenessEvaluation, error)
	UpsertOverlapAcknowledgment(ctx context.Context, ack OverlapAcknowledgment) error
	ListOverlapAcknowledgments(ctx context.Context, repoFullName string) ([]OverlapAcknowledgment, error)

	// PurgeRepository removes all stored records for a repository (e.g. on GitHub App uninstall/revocation).
	PurgeRepository(ctx context.Context, repoFullName string) error
	ListInstallationRepositories(context.Context, int64) ([]string, error)
	RegisterInstallationRepository(context.Context, int64, string) error

	Close() error
}

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/store"
)

var (
	// ErrFreshTurnRequired is returned when a protected action was held because evidence was
	// missing or stale. The adapter has injected the quoted project data and recorded a receipt;
	// the runtime must discard the pending edit and give the worker a fresh turn.
	ErrFreshTurnRequired = errors.New("evidence delivered; discard pending edit and retry on a fresh turn")

	// ErrActionHeld is returned when a protected action cannot proceed (e.g. private review
	// required or coordination index unavailable).
	ErrActionHeld = errors.New("protected action held")
)

// Client speaks Taaro Adapter Protocol v1 to taaro-server across integration levels 1-3.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient constructs a new Adapter Protocol v1 client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      strings.TrimSpace(token),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, respOut any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// List and handoff responses aggregate records independently of the request limit.
	const maxResponseBytes = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("server response %d exceeds the %d MiB client limit", resp.StatusCode, maxResponseBytes>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if respOut != nil {
		if err := json.Unmarshal(raw, respOut); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Level 1: Read and follow.

func (c *Client) GetContext(ctx context.Context, repo string, pr int) (store.PullRequestRecord, error) {
	var out store.PullRequestRecord
	path := fmt.Sprintf("/api/v1/context?repo=%s&pr=%d", url.QueryEscape(repo), pr)
	return out, c.doJSON(ctx, http.MethodGet, path, nil, &out)
}

func (c *Client) GetHandoff(ctx context.Context, repo string, pr int, audience string) (coordination.HandoffCard, error) {
	var out coordination.HandoffCard
	q := url.Values{"repo": {repo}, "pr": {strconv.Itoa(pr)}}
	if audience != "" {
		q.Set("audience", audience)
	}
	return out, c.doJSON(ctx, http.MethodGet, "/api/v1/handoff?"+q.Encode(), nil, &out)
}

func (c *Client) UpsertProposal(ctx context.Context, p store.ProposalRecord) (store.ProposalRecord, error) {
	var out store.ProposalRecord
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/proposals", p, &out)
}

func (c *Client) ListProposals(ctx context.Context, repo, audience string) ([]store.ProposalRecord, error) {
	var out struct {
		Proposals []store.ProposalRecord `json:"proposals"`
	}
	q := url.Values{"repo": {repo}}
	if audience != "" {
		q.Set("audience", audience)
	}
	return out.Proposals, c.doJSON(ctx, http.MethodGet, "/api/v1/proposals?"+q.Encode(), nil, &out)
}

func (c *Client) ListOverlaps(ctx context.Context, repo string, pr int, includeAck bool) ([]coordination.OverlapSuggestion, error) {
	var out struct {
		Overlaps []coordination.OverlapSuggestion `json:"overlaps"`
	}
	q := url.Values{"repo": {repo}}
	if pr > 0 {
		q.Set("pr", strconv.Itoa(pr))
	}
	if includeAck {
		q.Set("include_acknowledged", "1")
	}
	return out.Overlaps, c.doJSON(ctx, http.MethodGet, "/api/v1/overlaps?"+q.Encode(), nil, &out)
}

func (c *Client) AcknowledgeOverlap(ctx context.Context, repo string, prA, prB int, disposition, actor, note string) (store.OverlapAcknowledgment, error) {
	var out store.OverlapAcknowledgment
	req := map[string]any{
		"repo":        repo,
		"pr_a":        prA,
		"pr_b":        prB,
		"disposition": disposition,
		"actor":       actor,
		"note":        note,
	}
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/overlaps/ack", req, &out)
}

// Level 2: Exchange and subscribe.

type PublishArtifactResponse struct {
	Artifact       store.ArtifactRecord         `json:"artifact"`
	StaleConsumers []store.ArtifactSubscription `json:"stale_consumers"`
}

func (c *Client) PublishArtifact(ctx context.Context, art store.ArtifactRecord) (PublishArtifactResponse, error) {
	var out PublishArtifactResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/artifacts", art, &out)
}

func (c *Client) GetArtifact(ctx context.Context, repo, key string, version int) (store.ArtifactRecord, error) {
	var out store.ArtifactRecord
	q := url.Values{"repo": {repo}, "key": {key}}
	if version > 0 {
		q.Set("version", strconv.Itoa(version))
	}
	return out, c.doJSON(ctx, http.MethodGet, "/api/v1/artifacts?"+q.Encode(), nil, &out)
}

func (c *Client) ListArtifacts(ctx context.Context, repo string) ([]store.ArtifactRecord, error) {
	var out struct {
		Artifacts []store.ArtifactRecord `json:"artifacts"`
	}
	return out.Artifacts, c.doJSON(ctx, http.MethodGet, "/api/v1/artifacts?repo="+url.QueryEscape(repo), nil, &out)
}

func (c *Client) SubscribeArtifact(ctx context.Context, sub store.ArtifactSubscription) (store.ArtifactSubscription, error) {
	var out store.ArtifactSubscription
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/artifacts/subscribe", sub, &out)
}

func (c *Client) ListArtifactConsumers(ctx context.Context, repo, key string) ([]store.ArtifactSubscription, error) {
	var out struct {
		Consumers []store.ArtifactSubscription `json:"consumers"`
	}
	q := url.Values{"repo": {repo}, "key": {key}}
	return out.Consumers, c.doJSON(ctx, http.MethodGet, "/api/v1/artifacts/consumers?"+q.Encode(), nil, &out)
}

type RecordCheckResponse struct {
	Check        store.ValidationCheckRecord `json:"check"`
	CancelledIDs []int64                     `json:"cancelled_ids"`
}

func (c *Client) RecordCheck(ctx context.Context, rec store.ValidationCheckRecord) (RecordCheckResponse, error) {
	var out RecordCheckResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/checks", rec, &out)
}

func (c *Client) ListChecks(ctx context.Context, repo string, pr int) ([]store.ValidationCheckRecord, error) {
	var out struct {
		Checks []store.ValidationCheckRecord `json:"checks"`
	}
	q := url.Values{"repo": {repo}}
	if pr > 0 {
		q.Set("pr", strconv.Itoa(pr))
	}
	return out.Checks, c.doJSON(ctx, http.MethodGet, "/api/v1/checks?"+q.Encode(), nil, &out)
}

func (c *Client) MatchCheck(ctx context.Context, req coordination.MatchCheckRequest) (coordination.MatchCheckResponse, error) {
	var out coordination.MatchCheckResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/checks/match", req, &out)
}

// Level 3: Enforced awareness.

func (c *Client) RegisterScope(ctx context.Context, req coordination.RegisterScopeRequest) (coordination.RegisterScopeResponse, error) {
	body := map[string]any{
		"repo":               req.RepoFullName,
		"worker_id":          req.WorkerID,
		"context_generation": req.ContextGeneration,
		"parent_worker_id":   req.ParentWorkerID,
		"role":               req.Role,
		"pr_number":          req.PRNumber,
		"base_rev":           req.BaseRev,
		"issue_links":        req.IssueLinks,
		"paths":              req.Paths,
		"interfaces":         req.Interfaces,
		"ttl_seconds":        int(req.TTL.Seconds()),
		"audience":           req.Audience,
	}
	var out coordination.RegisterScopeResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/scopes", body, &out)
}

func (c *Client) EvaluateAction(ctx context.Context, req coordination.EvaluateActionRequest) (coordination.EvaluateActionResponse, error) {
	var out coordination.EvaluateActionResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/awareness/evaluate", req, &out)
}

func (c *Client) RecordReceipt(ctx context.Context, repo, workerID string, contextGen int, items []store.ReceiptItem) (store.DeliveryReceipt, error) {
	var out store.DeliveryReceipt
	req := map[string]any{
		"repo":               repo,
		"worker_id":          workerID,
		"context_generation": contextGen,
		"items":              items,
	}
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/awareness/receipt", req, &out)
}

func (c *Client) VerifyCandidate(ctx context.Context, req coordination.CandidateAwarenessRequest, publish bool) (coordination.CandidateAwarenessResponse, error) {
	body := struct {
		coordination.CandidateAwarenessRequest
		Publish bool `json:"publish,omitempty"`
	}{
		CandidateAwarenessRequest: req,
		Publish:                   publish,
	}
	var out coordination.CandidateAwarenessResponse
	return out, c.doJSON(ctx, http.MethodPost, "/api/v1/awareness/verify-pr", body, &out)
}

// WorkerSession is a Level 3 reference runtime adapter mediating a single worker's context generation.
type WorkerSession struct {
	Client            *Client
	Repo              string
	WorkerID          string
	ParentWorkerID    string
	Role              string
	ContextGeneration int
	Audience          string

	// DeliveredContext accumulates quoted project data injected into this worker's current generation.
	DeliveredContext []coordination.EvidenceItem
}

// NewWorkerSession creates a fresh Level 3 worker session at context generation 1.
func NewWorkerSession(client *Client, repo, workerID, role string) *WorkerSession {
	return &WorkerSession{
		Client:            client,
		Repo:              repo,
		WorkerID:          workerID,
		Role:              role,
		ContextGeneration: 1,
		Audience:          "authorized",
	}
}

// CompactOrReset advances the context generation and clears delivered context,
// ensuring subsequent protected actions re-deliver any relevant evidence.
func (w *WorkerSession) CompactOrReset() {
	w.ContextGeneration++
	w.DeliveredContext = nil
}

// Delegate creates a child worker with its own identity and context generation.
// The parent's delivery receipts never transfer to the delegated worker.
func (w *WorkerSession) Delegate(childWorkerID, role string) *WorkerSession {
	return &WorkerSession{
		Client:            w.Client,
		Repo:              w.Repo,
		WorkerID:          childWorkerID,
		ParentWorkerID:    w.WorkerID,
		Role:              role,
		ContextGeneration: 1,
		Audience:          w.Audience,
	}
}

// RegisterScope atomically registers this worker's active scope and queries immediate overlaps.
func (w *WorkerSession) RegisterScope(ctx context.Context, prNumber int, baseRev string, paths, interfaces []string, issues []int) (coordination.RegisterScopeResponse, error) {
	resp, err := w.Client.RegisterScope(ctx, coordination.RegisterScopeRequest{
		RepoFullName:      w.Repo,
		WorkerID:          w.WorkerID,
		ContextGeneration: w.ContextGeneration,
		ParentWorkerID:    w.ParentWorkerID,
		Role:              w.Role,
		PRNumber:          prNumber,
		BaseRev:           baseRev,
		Paths:             paths,
		Interfaces:        interfaces,
		IssueLinks:        issues,
		Audience:          w.Audience,
	})
	if err != nil || len(resp.Evidence) == 0 {
		return resp, err
	}
	for _, item := range resp.Evidence {
		if item.Redacted {
			return resp, fmt.Errorf("%w: active scope requires authorized review", ErrActionHeld)
		}
	}
	if err := w.deliverEvidence(ctx, resp.Evidence, resp.Evidence); err != nil {
		return resp, err
	}
	return resp, nil
}

// GuardedRead evaluates a read operation and injects any relevant quoted project data alongside it.
func (w *WorkerSession) GuardedRead(ctx context.Context, paths, interfaces []string, issues []int) (coordination.EvaluateActionResponse, error) {
	resp, err := w.Client.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      w.Repo,
		WorkerID:          w.WorkerID,
		ContextGeneration: w.ContextGeneration,
		Operation:         "read",
		TargetPaths:       paths,
		Interfaces:        interfaces,
		LinkedIssues:      issues,
		Audience:          w.Audience,
	})
	if err != nil {
		return coordination.EvaluateActionResponse{}, fmt.Errorf("%w: index unavailable: %v", ErrActionHeld, err)
	}
	switch resp.Decision {
	case "allow":
		return resp, nil
	case "allow_with_context":
		return resp, w.deliverEvidence(ctx, resp.MissingOrStaleEvidence, resp.Evidence)
	default:
		return resp, fmt.Errorf("%w: decision=%s reason=%s", ErrActionHeld, resp.Decision, resp.Reason)
	}
}

// MediatedWrite guards a write/plan/publish operation:
//   - If allowed, executes applyFn immediately.
//   - If held for missing/stale evidence, discards the pending edit (applyFn is NOT called),
//     inserts quoted project data into the worker's context, issues a delivery receipt,
//     and returns ErrFreshTurnRequired so the runtime gives the worker a fresh turn.
//   - If held for private review or unavailable index, fails closed with ErrActionHeld.
func (w *WorkerSession) MediatedWrite(ctx context.Context, operation string, paths, interfaces []string, issues []int, applyFn func() error) (coordination.EvaluateActionResponse, error) {
	operation = strings.ToLower(strings.TrimSpace(operation))
	switch operation {
	case "":
		operation = "write"
	case "write", "plan", "publish":
	default:
		return coordination.EvaluateActionResponse{}, fmt.Errorf("%w: unsupported write operation %q", ErrActionHeld, operation)
	}
	resp, err := w.Client.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      w.Repo,
		WorkerID:          w.WorkerID,
		ContextGeneration: w.ContextGeneration,
		Operation:         operation,
		TargetPaths:       paths,
		Interfaces:        interfaces,
		LinkedIssues:      issues,
		Audience:          w.Audience,
	})
	if err != nil {
		// Unavailable coordination index fails closed — never bypass mediation.
		return coordination.EvaluateActionResponse{Decision: "pending"}, fmt.Errorf("%w: %v", ErrActionHeld, err)
	}

	switch resp.Decision {
	case "allow":
		if applyFn != nil {
			if err := applyFn(); err != nil {
				return resp, err
			}
		}
		return resp, nil

	case "hold":
		// Insert evidence as quoted project data, record trusted receipt AFTER insertion succeeds,
		// discard pending edit, and signal the runtime to give the worker a fresh turn.
		if err := w.deliverEvidence(ctx, resp.MissingOrStaleEvidence, resp.Evidence); err != nil {
			return resp, err
		}
		return resp, ErrFreshTurnRequired

	default:
		// "review_required" or "pending"
		return resp, fmt.Errorf("%w: decision=%s reason=%s", ErrActionHeld, resp.Decision, resp.Reason)
	}
}

func (w *WorkerSession) deliverEvidence(ctx context.Context, updates, evidence []coordination.EvidenceItem) error {
	for _, it := range updates {
		it.QuotedData = true
		w.DeliveredContext = append(w.DeliveredContext, it)
	}
	if _, err := w.Client.RecordReceipt(ctx, w.Repo, w.WorkerID, w.ContextGeneration, coordination.ReceiptItemsFromEvidence(evidence)); err != nil {
		return fmt.Errorf("%w: failed recording delivery receipt: %v", ErrActionHeld, err)
	}
	return nil
}

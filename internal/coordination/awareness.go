package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
)

// EvidenceItem represents a single piece of quoted project evidence delivered to an acting worker.
// All proposal and work excerpts are marked QuotedData=true with zero authority to alter permissions or policy.
type EvidenceItem struct {
	Kind           string    `json:"kind"` // "proposal", "scope", "pr", "artifact"
	Key            string    `json:"key"`
	Version        string    `json:"version"`
	LifecycleState string    `json:"lifecycle_state,omitempty"`
	Title          string    `json:"title,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	QuotedExcerpt  string    `json:"quoted_excerpt,omitempty"`
	QuotedData     bool      `json:"quoted_data"`
	SourceLink     string    `json:"source_link,omitempty"`
	MatchReason    string    `json:"match_reason"`
	Authority      string    `json:"authority"` // "authoritative_source", "taaro_suggestion", "platform_observation", "producer_artifact"
	Redacted       bool      `json:"redacted,omitempty"`
	ObservedAt     time.Time `json:"observed_at,omitempty"`
}

// RegisterScopeRequest registers a worker's active scope and queries overlaps atomically.
type RegisterScopeRequest struct {
	RepoFullName      string        `json:"repo"`
	WorkerID          string        `json:"worker_id"`
	ContextGeneration int           `json:"context_generation"`
	ParentWorkerID    string        `json:"parent_worker_id,omitempty"`
	Role              string        `json:"role,omitempty"`
	PRNumber          int           `json:"pr_number,omitempty"`
	BaseRev           string        `json:"base_rev,omitempty"`
	IssueLinks        []int         `json:"issue_links,omitempty"`
	Paths             []string      `json:"paths,omitempty"`
	Interfaces        []string      `json:"interfaces,omitempty"`
	TTL               time.Duration `json:"ttl_seconds,omitempty"`
	Audience          string        `json:"audience,omitempty"` // "authorized" or "public"
}

// RegisterScopeResponse returns the persisted scope and all atomically discovered matches.
type RegisterScopeResponse struct {
	Scope           store.WorkerScopeRecord `json:"scope"`
	Evidence        []EvidenceItem          `json:"evidence"`
	EvidenceSetHash string                  `json:"evidence_set_hash"`
	Silent          bool                    `json:"silent"`
	EvaluatedAt     time.Time               `json:"evaluated_at"`
}

// EvaluateActionRequest describes a protected runtime operation to mediate.
type EvaluateActionRequest struct {
	RepoFullName      string   `json:"repo"`
	WorkerID          string   `json:"worker_id"`
	ContextGeneration int      `json:"context_generation"`
	Operation         string   `json:"operation"` // "read", "write", "plan", "publish"
	PRNumber          int      `json:"pr_number,omitempty"`
	HeadSHA           string   `json:"head_sha,omitempty"`
	TargetPaths       []string `json:"target_paths,omitempty"`
	Interfaces        []string `json:"interfaces,omitempty"`
	LinkedIssues      []int    `json:"linked_issues,omitempty"`
	Audience          string   `json:"audience,omitempty"` // "authorized" or "public"
}

// EvaluateActionResponse returns the mediation decision for a protected operation.
type EvaluateActionResponse struct {
	Decision               string         `json:"decision"` // "allow", "allow_with_context", "hold", "review_required", "pending"
	Silent                 bool           `json:"silent"`
	DiscardPendingEdit     bool           `json:"discard_pending_edit,omitempty"`
	RequireFreshTurn       bool           `json:"require_fresh_turn,omitempty"`
	Reason                 string         `json:"reason,omitempty"`
	WorkerID               string         `json:"worker_id"`
	ContextGeneration      int            `json:"context_generation"`
	EvidenceSetHash        string         `json:"evidence_set_hash"`
	Evidence               []EvidenceItem `json:"evidence,omitempty"`
	MissingOrStaleEvidence []EvidenceItem `json:"missing_or_stale_evidence,omitempty"`
	ReceiptVerified        bool           `json:"receipt_verified"`
	EvaluatedAt            time.Time      `json:"evaluated_at"`
}

// CandidateAwarenessRequest evaluates a PR or merge-group head SHA for the required Taaro Awareness Check.
type CandidateAwarenessRequest struct {
	RepoFullName      string   `json:"repo"`
	PRNumber          int      `json:"pr_number,omitempty"`
	HeadSHA           string   `json:"head_sha"`
	RevisionKind      string   `json:"revision_kind,omitempty"` // "pr_head" or "merge_group"
	WorkerID          string   `json:"worker_id,omitempty"`
	ContextGeneration int      `json:"context_generation,omitempty"`
	TargetPaths       []string `json:"target_paths"`
	Interfaces        []string `json:"interfaces,omitempty"`
	LinkedIssues      []int    `json:"linked_issues,omitempty"`
	Audience          string   `json:"audience,omitempty"`
}

// CandidateAwarenessResponse reports the complete/incomplete required Awareness Check verdict.
type CandidateAwarenessResponse struct {
	Evaluation store.AwarenessEvaluation `json:"evaluation"`
	Evidence   []EvidenceItem            `json:"evidence,omitempty"`
}

// Engine coordinates awareness mediation, overlap detection, check reuse, and handoff assembly.
type Engine struct {
	Store store.Store
	Now   func() time.Time

	// AwarenessPublication orders candidate publication and proposal refresh
	// within a server process.
	AwarenessPublication sync.Mutex
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// RegisterScope atomically records a worker's active scope and returns all matching
// proposals, sibling active scopes, and open PRs in one serialized store transaction.
func (e *Engine) RegisterScope(ctx context.Context, req RegisterScopeRequest) (RegisterScopeResponse, error) {
	if req.RepoFullName == "" || req.WorkerID == "" || req.ContextGeneration <= 0 {
		return RegisterScopeResponse{}, errors.New("repo, worker_id, and positive context_generation are required")
	}
	now := e.now()
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}

	scopeRec := store.WorkerScopeRecord{
		RepoFullName:      req.RepoFullName,
		WorkerID:          req.WorkerID,
		ContextGeneration: req.ContextGeneration,
		ParentWorkerID:    req.ParentWorkerID,
		Role:              req.Role,
		PRNumber:          req.PRNumber,
		BaseRev:           req.BaseRev,
		IssueLinks:        req.IssueLinks,
		Paths:             FilterNonIncidentalPaths(req.Paths),
		Interfaces:        req.Interfaces,
		RegisteredAt:      now,
		ExpiresAt:         now.Add(ttl),
	}

	snap, err := e.Store.RegisterScopeAndSnapshot(ctx, scopeRec, now)
	if err != nil {
		return RegisterScopeResponse{}, err
	}

	subs, err := e.Store.ListWorkerArtifactSubscriptions(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
	if err != nil {
		return RegisterScopeResponse{}, err
	}
	priorReceipts, err := e.Store.ListWorkerReceipts(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
	if err != nil {
		return RegisterScopeResponse{}, err
	}

	items, _, err := e.matchEvidence(ctx, matchInput{
		repo:          req.RepoFullName,
		workerID:      req.WorkerID,
		prNumber:      req.PRNumber,
		paths:         scopeRec.Paths,
		interfaces:    scopeRec.Interfaces,
		linkedIssues:  scopeRec.IssueLinks,
		audience:      normalizeAudience(req.Audience),
		proposals:     snap.Proposals,
		activeScopes:  snap.ActiveScopes,
		openPRs:       snap.OpenPRs,
		subscriptions: subs,
		priorReceipts: priorReceipts,
	})

	if err != nil {
		return RegisterScopeResponse{}, err
	}
	hash := ComputeEvidenceSetHash(items)
	return RegisterScopeResponse{
		Scope:           snap.Scope,
		Evidence:        items,
		EvidenceSetHash: hash,
		Silent:          len(items) == 0,
		EvaluatedAt:     now,
	}, nil
}

// EvaluateAction mediates a protected runtime operation (read, write, plan, publish).
// It enforces per-(worker_id, context_generation) delivery receipts, membership + version
// sensitivity, private source redaction, fail-closed index errors, and zero-friction silent
// clearance when a complete lookup finds no relevant matches.
func (e *Engine) EvaluateAction(ctx context.Context, req EvaluateActionRequest) (EvaluateActionResponse, error) {
	now := e.now()
	op := strings.ToLower(strings.TrimSpace(req.Operation))
	if op == "" {
		op = "write"
	}
	switch op {
	case "read", "write", "plan", "publish":
	default:
		return EvaluateActionResponse{}, fmt.Errorf("unsupported operation %q (want read, write, plan, or publish)", req.Operation)
	}
	if req.RepoFullName == "" || req.WorkerID == "" || req.ContextGeneration <= 0 {
		return EvaluateActionResponse{}, errors.New("repo, worker_id, and positive context_generation are required")
	}

	pendingResp := func(reason string) EvaluateActionResponse {
		return EvaluateActionResponse{
			Decision:          "pending",
			Silent:            false,
			Reason:            reason,
			WorkerID:          req.WorkerID,
			ContextGeneration: req.ContextGeneration,
			EvaluatedAt:       now,
		}
	}

	if e.Store == nil {
		return pendingResp("coordination index unavailable"), nil
	}

	proposals, err := e.Store.ListProposals(ctx, req.RepoFullName)
	if err != nil {
		return pendingResp("proposal index lookup failed"), nil
	}
	activeScopes, err := e.Store.ListActiveScopes(ctx, req.RepoFullName, now)
	if err != nil {
		return pendingResp("active scope lookup failed"), nil
	}
	openPRs, err := e.Store.ListOpenPullRequests(ctx, req.RepoFullName)
	if err != nil {
		return pendingResp("pull request index lookup failed"), nil
	}
	subs, err := e.Store.ListWorkerArtifactSubscriptions(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
	if err != nil {
		return pendingResp("artifact subscription lookup failed"), nil
	}
	priorReceipts, err := e.Store.ListWorkerReceipts(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
	if err != nil {
		return pendingResp("delivery receipt lookup failed"), nil
	}

	// Merge any registered scope attributes for this exact (worker_id, context_generation)
	// when the worker omitted them on a subsequent operation.
	paths := FilterNonIncidentalPaths(req.TargetPaths)
	hasTargets := len(req.TargetPaths) > 0 || len(req.Interfaces) > 0 || len(req.LinkedIssues) > 0
	interfaces := req.Interfaces
	issues := req.LinkedIssues
	prNum := req.PRNumber
	for _, sc := range activeScopes {
		if sc.WorkerID == req.WorkerID && sc.ContextGeneration == req.ContextGeneration {
			if req.TargetPaths == nil {
				paths = FilterNonIncidentalPaths(sc.Paths)
				hasTargets = hasTargets || len(sc.Paths) > 0
			}
			if len(interfaces) == 0 {
				interfaces = sc.Interfaces
			}
			if len(issues) == 0 {
				issues = sc.IssueLinks
			}
			if prNum == 0 {
				prNum = sc.PRNumber
			}
			break
		}
	}

	if op != "read" && !hasTargets && len(interfaces) == 0 && len(issues) == 0 {
		return pendingResp("protected operation target is unknown"), nil
	}

	evidence, hasRedactedPrivate, err := e.matchEvidence(ctx, matchInput{
		repo:          req.RepoFullName,
		workerID:      req.WorkerID,
		prNumber:      prNum,
		paths:         paths,
		interfaces:    interfaces,
		linkedIssues:  issues,
		audience:      normalizeAudience(req.Audience),
		proposals:     proposals,
		activeScopes:  activeScopes,
		openPRs:       openPRs,
		subscriptions: subs,
		priorReceipts: priorReceipts,
	})

	if err != nil {
		return pendingResp("artifact evidence lookup incomplete"), nil
	}
	evidenceHash := ComputeEvidenceSetHash(evidence)

	// Routine fix path: complete lookup with zero matches clears silently with zero prompts!
	if len(evidence) == 0 {
		return EvaluateActionResponse{
			Decision:          "allow",
			Silent:            true,
			Reason:            "complete lookup found no relevant overlaps, proposals, or stale fixtures",
			WorkerID:          req.WorkerID,
			ContextGeneration: req.ContextGeneration,
			EvidenceSetHash:   evidenceHash,
			ReceiptVerified:   true,
			EvaluatedAt:       now,
		}, nil
	}

	// Private source redaction: hold without revealing private titles, paths, or excerpts.
	if hasRedactedPrivate {
		return EvaluateActionResponse{
			Decision:               "review_required",
			Silent:                 false,
			DiscardPendingEdit:     op != "read",
			RequireFreshTurn:       op != "read",
			Reason:                 "applicable private source requires authorized review; contents withheld",
			WorkerID:               req.WorkerID,
			ContextGeneration:      req.ContextGeneration,
			EvidenceSetHash:        evidenceHash,
			Evidence:               evidence,
			MissingOrStaleEvidence: evidence,
			ReceiptVerified:        false,
			EvaluatedAt:            now,
		}, nil
	}

	// Check whether this exact (worker_id, context_generation) already holds a receipt
	// covering the complete current matching set AND exact versions.
	covered := false
	var missing []EvidenceItem
	if len(priorReceipts) > 0 {
		latest := priorReceipts[0]
		if latest.EvidenceSetHash == evidenceHash {
			covered = true
		} else {
			deliveredMap := make(map[[2]string][2]string, len(latest.Items))
			for _, it := range latest.Items {
				deliveredMap[[2]string{it.Kind, it.Key}] = [2]string{it.Version, it.State}
			}
			for _, ev := range evidence {
				if deliveredMap[[2]string{ev.Kind, ev.Key}] != [2]string{ev.Version, ev.LifecycleState} {
					missing = append(missing, ev)
				}
			}
			// Even if all current items were seen individually in older receipts, membership
			// or state differences require a fresh receipt matching evidenceHash.
			if len(missing) == 0 {
				missing = evidence
			}
		}
	} else {
		missing = evidence
	}

	if covered {
		return EvaluateActionResponse{
			Decision:          "allow",
			Silent:            true,
			Reason:            "current evidence already delivered and verified for this worker context generation",
			WorkerID:          req.WorkerID,
			ContextGeneration: req.ContextGeneration,
			EvidenceSetHash:   evidenceHash,
			Evidence:          evidence,
			ReceiptVerified:   true,
			EvaluatedAt:       now,
		}, nil
	}

	// Guarded read returns context alongside the read without blocking it.
	if op == "read" {
		return EvaluateActionResponse{
			Decision:               "allow_with_context",
			Silent:                 false,
			Reason:                 "returning relevant quoted context alongside guarded read",
			WorkerID:               req.WorkerID,
			ContextGeneration:      req.ContextGeneration,
			EvidenceSetHash:        evidenceHash,
			Evidence:               evidence,
			MissingOrStaleEvidence: missing,
			ReceiptVerified:        false,
			EvaluatedAt:            now,
		}, nil
	}

	// Guarded write / plan / publish: hold action, deliver quoted evidence, discard pending edit,
	// require receipt after insertion and a fresh worker turn.
	return EvaluateActionResponse{
		Decision:               "hold",
		Silent:                 false,
		DiscardPendingEdit:     true,
		RequireFreshTurn:       true,
		Reason:                 "missing or stale evidence for protected action; deliver quoted data, record receipt, and retry on a fresh turn",
		WorkerID:               req.WorkerID,
		ContextGeneration:      req.ContextGeneration,
		EvidenceSetHash:        evidenceHash,
		Evidence:               evidence,
		MissingOrStaleEvidence: missing,
		ReceiptVerified:        false,
		EvaluatedAt:            now,
	}, nil
}

// RecordReceipt records verified delivery of an evidence set to a specific (worker_id, context_generation).
func (e *Engine) RecordReceipt(ctx context.Context, repo, workerID string, contextGen int, items []store.ReceiptItem) (store.DeliveryReceipt, error) {
	if repo == "" || workerID == "" || contextGen <= 0 {
		return store.DeliveryReceipt{}, errors.New("repo, worker_id, and positive context_generation are required")
	}
	hash := ComputeReceiptItemsHash(items)
	return e.Store.RecordDeliveryReceipt(ctx, store.DeliveryReceipt{
		RepoFullName:      repo,
		WorkerID:          workerID,
		ContextGeneration: contextGen,
		EvidenceSetHash:   hash,
		Items:             items,
		DeliveredAt:       e.now(),
	})
}

// EvaluateCandidate evaluates a candidate PR or merge-group SHA for the required Taaro Awareness Check.
// Per docs/PROJECT.md §3, "success" is emitted ONLY after a complete, current evaluation where
// either zero matches exist or a current delivery receipt covers all matching evidence.
// Incomplete evaluations NEVER use "neutral" or "skipped".
func (e *Engine) EvaluateCandidate(ctx context.Context, req CandidateAwarenessRequest) (CandidateAwarenessResponse, error) {
	now := e.now()
	if req.RepoFullName == "" || req.HeadSHA == "" {
		return CandidateAwarenessResponse{}, errors.New("repo and head_sha are required")
	}
	revKind := req.RevisionKind
	if revKind == "" {
		revKind = "pr_head"
	}

	if revKind != "pr_head" && revKind != "merge_group" {
		return CandidateAwarenessResponse{}, fmt.Errorf("unsupported revision_kind %q", revKind)
	}
	if e.Store == nil {
		return CandidateAwarenessResponse{}, errors.New("coordination index unavailable")
	}

	eval := store.AwarenessEvaluation{
		RepoFullName: req.RepoFullName, PRNumber: req.PRNumber, HeadSHA: req.HeadSHA,
		RevisionKind: revKind, WorkerID: req.WorkerID, ContextGeneration: req.ContextGeneration,
		IndexedAt: now, ValidatedAt: now,
	}
	pending := func(reason string) (CandidateAwarenessResponse, error) {
		eval.Verdict = "in_progress"
		eval.Reason = reason
		recorded, err := e.Store.RecordAwarenessEvaluation(ctx, eval)
		return CandidateAwarenessResponse{Evaluation: recorded}, err
	}

	if indexed, ok := e.Store.(interface {
		CurrentProposalAwarenessWatermark(context.Context) (int64, error)
	}); ok {
		var err error
		eval.ProposalWatermark, err = indexed.CurrentProposalAwarenessWatermark(ctx)
		if err != nil {
			return pending("proposal index snapshot unavailable")
		}
	}

	paths := req.TargetPaths
	issues := req.LinkedIssues
	if revKind == "pr_head" {
		pr, err := e.Store.GetPullRequest(ctx, req.RepoFullName, req.PRNumber)
		if err != nil || pr.HeadSHA != req.HeadSHA || pr.Paths == nil {
			return pending("current candidate diff is unavailable in the pull request index")
		}
		paths = append(append([]string{}, pr.Paths...), paths...)
		issues = append(append([]int{}, pr.LinkedIssues...), issues...)
	} else if paths == nil {
		return pending("current merge-group diff is unavailable")
	}

	proposals, err := e.Store.ListProposals(ctx, req.RepoFullName)
	if err != nil {
		return pending("proposal index lookup incomplete")
	}
	activeScopes, err := e.Store.ListActiveScopes(ctx, req.RepoFullName, now)
	if err != nil {
		return pending("scope index lookup incomplete")
	}
	openPRs, err := e.Store.ListOpenPullRequests(ctx, req.RepoFullName)
	if err != nil {
		return pending("pull request index lookup incomplete")
	}

	var subs []store.ArtifactSubscription
	var priorReceipts []store.DeliveryReceipt
	if req.WorkerID != "" && req.ContextGeneration > 0 {
		subs, err = e.Store.ListWorkerArtifactSubscriptions(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
		if err != nil {
			return pending("artifact subscription lookup incomplete")
		}
		priorReceipts, err = e.Store.ListWorkerReceipts(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration)
		if err != nil {
			return pending("delivery receipt lookup incomplete")
		}
	}

	evidence, hasRedactedPrivate, err := e.matchEvidence(ctx, matchInput{
		repo:          req.RepoFullName,
		workerID:      req.WorkerID,
		prNumber:      req.PRNumber,
		paths:         paths,
		interfaces:    req.Interfaces,
		linkedIssues:  issues,
		audience:      normalizeAudience(req.Audience),
		proposals:     proposals,
		activeScopes:  activeScopes,
		openPRs:       openPRs,
		subscriptions: subs,
		priorReceipts: priorReceipts,
	})
	if err != nil {
		return pending("artifact evidence lookup incomplete")
	}
	hash := ComputeEvidenceSetHash(evidence)

	verdict := "action_required"
	reason := ""

	switch {
	case hasRedactedPrivate:
		verdict = "action_required"
		reason = "applicable private proposal requires authorized review before merge"
	case len(evidence) == 0:
		verdict = "success"
		reason = fmt.Sprintf("complete awareness evaluation on %s (%s): no applicable proposals, overlaps, or stale fixtures", shortSHA(req.HeadSHA), revKind)
	case req.WorkerID == "" || req.ContextGeneration <= 0:
		verdict = "action_required"
		reason = fmt.Sprintf("candidate matches %d evidence item(s) but lacks a verified worker delivery receipt (uninstrumented contribution requires supported review)", len(evidence))
	case len(priorReceipts) > 0 && priorReceipts[0].EvidenceSetHash == hash:
		verdict = "success"
		reason = fmt.Sprintf("verified delivery receipt for %s@gen%d covers all %d evidence item(s) on %s (%s)", req.WorkerID, req.ContextGeneration, len(evidence), shortSHA(req.HeadSHA), revKind)
	default:
		verdict = "action_required"
		reason = fmt.Sprintf("delivery receipt for %s@gen%d is missing or stale for %d matching evidence item(s)", req.WorkerID, req.ContextGeneration, len(evidence))
	}

	eval.EvidenceSetHash = hash
	eval.Verdict = verdict
	eval.Complete = true
	eval.Reason = reason
	eval, err = e.Store.RecordAwarenessEvaluation(ctx, eval)
	if err != nil {
		return CandidateAwarenessResponse{}, err
	}

	return CandidateAwarenessResponse{
		Evaluation: eval,
		Evidence:   evidence,
	}, nil
}

// RecordAwarenessPublication binds the published GitHub result to its evaluation.
func (e *Engine) RecordAwarenessPublication(ctx context.Context, eval store.AwarenessEvaluation, checkID int64) error {
	if indexed, ok := e.Store.(interface {
		RecordAwarenessPublication(context.Context, store.AwarenessEvaluation, int64) error
	}); ok {
		return indexed.RecordAwarenessPublication(ctx, eval, checkID)
	}
	return e.Store.RecordAwarenessCheckRun(ctx, eval.RepoFullName, eval.PRNumber, eval.HeadSHA, checkID, eval.Verdict)
}

type matchInput struct {
	repo          string
	workerID      string
	prNumber      int
	paths         []string
	interfaces    []string
	linkedIssues  []int
	audience      string
	proposals     []store.ProposalRecord
	activeScopes  []store.WorkerScopeRecord
	openPRs       []store.PullRequestRecord
	subscriptions []store.ArtifactSubscription
	priorReceipts []store.DeliveryReceipt
}

func (e *Engine) matchEvidence(ctx context.Context, in matchInput) ([]EvidenceItem, bool, error) {
	var items []EvidenceItem
	hasRedactedPrivate := false

	// Track which proposals this worker previously received so withdrawal/supersession
	// triggers a lifecycle update notice to prior recipients (docs/PROJECT.md §3).
	lastDeliveredProposal := make(map[string]string)
	for _, r := range in.priorReceipts {
		for _, it := range r.Items {
			if it.Kind == "proposal" && lastDeliveredProposal[it.Key] == "" {
				lastDeliveredProposal[it.Key] = it.Version
			}
		}
	}
	publicProposals := make(map[string]bool, len(in.proposals))
	for _, p := range in.proposals {
		publicProposals[p.ProposalKey] = p.Visibility == "public"
	}

	// 1. Proposals (explicit issue links, declared scope paths, or named interfaces).
	for _, p := range in.proposals {
		version := fmt.Sprintf("v%d", p.Version)
		matchReason := proposalMatchReason(p, in.paths, in.interfaces, in.linkedIssues)
		// Withdrawn/superseded proposals end applicability for new workers, but must notify prior recipients.
		if p.State == "withdrawn" || p.State == "superseded" {
			lastDelivered := lastDeliveredProposal[p.ProposalKey]
			if lastDelivered == "" || (matchReason == "" && lastDelivered == version) {
				continue
			}
			matchReason = "lifecycle_update:" + p.State
		}
		if matchReason == "" {
			continue
		}

		if p.Visibility != "public" && in.audience != "authorized" {
			hasRedactedPrivate = true
			continue
		}

		authority := "taaro_suggestion"
		if p.State == "accepted" {
			authority = "authoritative_source"
		}
		summary := p.Summary
		if p.State == "superseded" && p.SupersededBy != "" && (in.audience == "authorized" || publicProposals[p.SupersededBy]) {
			summary = strings.TrimSpace(summary + fmt.Sprintf(" [superseded by %s]", p.SupersededBy))
		}
		items = append(items, EvidenceItem{
			Kind:           "proposal",
			Key:            p.ProposalKey,
			Version:        version,
			LifecycleState: p.State,
			Title:          p.Title,
			Summary:        summary,
			QuotedExcerpt:  p.Excerpt,
			QuotedData:     true,
			SourceLink:     p.SourceURL,
			MatchReason:    matchReason,
			Authority:      authority,
			ObservedAt:     p.ObservedAt,
		})
	}

	if hasRedactedPrivate {
		items = append(items, EvidenceItem{
			Kind:        "proposal",
			QuotedData:  true,
			MatchReason: "private_source_match",
			Authority:   "authoritative_source",
			Redacted:    true,
		})
	}

	// 2. Sibling active worker scopes.
	for _, sc := range in.activeScopes {
		if sc.WorkerID == in.workerID {
			continue
		}
		reason := scopeMatchReason(sc, in.paths, in.interfaces, in.linkedIssues)
		if reason == "" {
			continue
		}
		items = append(items, EvidenceItem{
			Kind:           "scope",
			Key:            fmt.Sprintf("%s@gen%d", sc.WorkerID, sc.ContextGeneration),
			Version:        scopeVersion(sc),
			LifecycleState: "active",
			Title:          fmt.Sprintf("Active worker %s (%s)", sc.WorkerID, sc.Role),
			Summary:        fmt.Sprintf("base_rev=%s pr=#%d", shortSHA(sc.BaseRev), sc.PRNumber),
			QuotedData:     true,
			MatchReason:    reason,
			Authority:      "platform_observation",
			ObservedAt:     sc.RegisteredAt,
		})
	}

	// 3. Accessible open/draft PRs sharing explicit issue links or declared non-incidental paths.
	for _, pr := range in.openPRs {
		if in.prNumber > 0 && pr.Number == in.prNumber {
			continue
		}
		var reasons []string
		if shared := SharedInts(pr.LinkedIssues, in.linkedIssues); len(shared) > 0 {
			reasons = append(reasons, fmt.Sprintf("shared_issue:#%d", shared[0]))
		}
		if ok, p := PathsOverlap(pr.Paths, in.paths); ok {
			reasons = append(reasons, fmt.Sprintf("shared_path:%s", p))
		}
		if len(reasons) == 0 {
			continue
		}
		state := pr.State
		if pr.Draft {
			state = "draft"
		}
		items = append(items, EvidenceItem{
			Kind:           "pr",
			Key:            fmt.Sprintf("pr#%d", pr.Number),
			Version:        pr.HeadSHA,
			LifecycleState: state,
			Title:          pr.Title,
			Summary:        fmt.Sprintf("PR #%d on %s (%s)", pr.Number, shortSHA(pr.HeadSHA), state),
			QuotedData:     true,
			SourceLink:     pr.HTMLURL,
			MatchReason:    strings.Join(reasons, ", "),
			Authority:      "platform_observation",
			ObservedAt:     pr.ObservedAt,
		})
	}

	// 4. Subscribed/consumed artifacts that now have a newer immutable version available!
	for _, sub := range in.subscriptions {
		if !sub.Stale || sub.LatestVersion <= 0 {
			continue
		}
		art, err := e.Store.GetArtifact(ctx, in.repo, sub.ArtifactKey, sub.LatestVersion)
		if err != nil {
			return nil, false, fmt.Errorf("lookup revised artifact: %w", err)
		}
		items = append(items, EvidenceItem{
			Kind:           "artifact",
			Key:            art.ArtifactKey,
			Version:        fmt.Sprintf("v%d", art.Version),
			LifecycleState: "revised",
			Title:          fmt.Sprintf("Artifact %s updated to v%d (consumed v%d)", art.ArtifactKey, art.Version, sub.ConsumedVersion),
			Summary:        art.Summary,
			QuotedExcerpt:  art.Payload,
			QuotedData:     true,
			MatchReason:    fmt.Sprintf("subscribed_artifact_revised:v%d->v%d", sub.ConsumedVersion, art.Version),
			Authority:      "producer_artifact",
			ObservedAt:     art.CreatedAt,
		})
	}

	slices.SortFunc(items, func(a, b EvidenceItem) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Key, b.Key)
	})
	return items, hasRedactedPrivate, nil
}

func proposalMatchReason(p store.ProposalRecord, paths, interfaces []string, issues []int) string {
	var reasons []string
	if shared := SharedInts(p.LinkedIssues, issues); len(shared) > 0 {
		reasons = append(reasons, fmt.Sprintf("linked_issue:#%d", shared[0]))
	}
	if shared := SharedStrings(p.Interfaces, interfaces); len(shared) > 0 {
		reasons = append(reasons, fmt.Sprintf("interface:%s", shared[0]))
	}
	if ok, matched := PathsOverlap(p.ScopePaths, paths); ok {
		reasons = append(reasons, fmt.Sprintf("scope_path:%s", matched))
	}
	return strings.Join(reasons, ", ")
}

// ProposalAffectsPullRequest reports whether a proposal's indexed scope can affect
// a candidate. A previously successful candidate without a known diff needs review
// when a proposal has file selectors.
func ProposalAffectsPullRequest(p store.ProposalRecord, pr store.PullRequestRecord) bool {
	switch p.State {
	case "", "withdrawn", "superseded":
		return false
	}
	if len(p.ScopePaths) > 0 && pr.Paths == nil {
		return true
	}
	return proposalMatchReason(p, pr.Paths, nil, pr.LinkedIssues) != ""
}

func scopeVersion(sc store.WorkerScopeRecord) string {
	// Renewing a lease preserves the delivered scope.
	sc.ExpiresAt = time.Time{}
	encoded, _ := json.Marshal(sc)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func scopeMatchReason(sc store.WorkerScopeRecord, paths, interfaces []string, issues []int) string {
	var reasons []string
	if shared := SharedInts(sc.IssueLinks, issues); len(shared) > 0 {
		reasons = append(reasons, fmt.Sprintf("shared_issue:#%d", shared[0]))
	}
	if shared := SharedStrings(sc.Interfaces, interfaces); len(shared) > 0 {
		reasons = append(reasons, fmt.Sprintf("shared_interface:%s", shared[0]))
	}
	if ok, matched := PathsOverlap(sc.Paths, paths); ok {
		reasons = append(reasons, fmt.Sprintf("shared_path:%s", matched))
	}
	return strings.Join(reasons, ", ")
}

// ComputeEvidenceSetHash produces a deterministic hash over matching-set membership,
// item versions, and lifecycle states so any addition, removal, or revision invalidates prior receipts.
func ComputeEvidenceSetHash(items []EvidenceItem) string {
	return ComputeReceiptItemsHash(ReceiptItemsFromEvidence(items))
}

// ComputeReceiptItemsHash computes the canonical evidence-set hash from receipt items.
func ComputeReceiptItemsHash(items []store.ReceiptItem) string {
	cp := make([]store.ReceiptItem, len(items))
	copy(cp, items)
	slices.SortFunc(cp, func(a, b store.ReceiptItem) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if a.Key != b.Key {
			return strings.Compare(a.Key, b.Key)
		}
		if a.Version != b.Version {
			return strings.Compare(a.Version, b.Version)
		}
		return strings.Compare(a.State, b.State)
	})
	encoded, _ := json.Marshal(cp)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReceiptItemsFromEvidence converts evaluated evidence into receipt items for delivery attestation.
func ReceiptItemsFromEvidence(items []EvidenceItem) []store.ReceiptItem {
	out := make([]store.ReceiptItem, 0, len(items))
	for _, it := range items {
		out = append(out, store.ReceiptItem{
			Kind:    it.Kind,
			Key:     it.Key,
			Version: it.Version,
			State:   it.LifecycleState,
		})
	}
	return out
}

func normalizeAudience(audience string) string {
	if strings.EqualFold(strings.TrimSpace(audience), "public") {
		return "public"
	}
	return "authorized"
}

func redactedKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "private:" + hex.EncodeToString(sum[:6])
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

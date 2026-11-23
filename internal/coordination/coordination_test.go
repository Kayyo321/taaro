package coordination_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/store"
)

func newTestEngine(t *testing.T) (*coordination.Engine, *store.SQLiteStore) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	return &coordination.Engine{
		Store: st,
		Now:   func() time.Time { return now },
	}, st
}

func TestRoutineFixClearsSilently(t *testing.T) {
	eng, _ := newTestEngine(t)
	ctx := context.Background()

	// A spelling correction in a CLI help string with no matching proposals/overlaps clears silently.
	resp, err := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "worker-typo",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"cmd/widgets/help.go"},
	})
	if err != nil {
		t.Fatalf("EvaluateAction: %v", err)
	}
	if resp.Decision != "allow" || !resp.Silent || !resp.ReceiptVerified {
		t.Fatalf("expected silent allow for routine fix, got %+v", resp)
	}
}

func TestOneLinePublicContractChangeTriggersDelivery(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()

	if _, err := st.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: "acme/widgets",
		ProposalKey:  "rfc/expiry-units",
		SourceType:   "rfc",
		SourceRef:    "docs/rfcs/004-expiry.md",
		Title:        "Cache expiry field uses seconds",
		State:        "accepted",
		Interfaces:   []string{"CacheConfig.Expiry"},
		Summary:      "Registered client cache expects seconds, not milliseconds.",
		Excerpt:      "Expiry MUST remain seconds in v1.",
		Visibility:   "public",
	}); err != nil {
		t.Fatalf("UpsertProposal: %v", err)
	}

	// Guarded write touching the public contract interface must HOLD with quoted project data.
	hold, err := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "worker-api",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"pkg/cache/config.go"},
		Interfaces:        []string{"CacheConfig.Expiry"},
	})
	if err != nil {
		t.Fatalf("EvaluateAction: %v", err)
	}
	if hold.Decision != "hold" || !hold.DiscardPendingEdit || !hold.RequireFreshTurn {
		t.Fatalf("expected hold with discard + fresh turn, got %+v", hold)
	}
	if len(hold.MissingOrStaleEvidence) != 1 || !hold.MissingOrStaleEvidence[0].QuotedData {
		t.Fatalf("expected quoted evidence item, got %+v", hold.MissingOrStaleEvidence)
	}

	// Record delivery receipt for (worker-api, gen 1), then fresh turn clears.
	if _, err := eng.RecordReceipt(ctx, "acme/widgets", "worker-api", 1, coordination.ReceiptItemsFromEvidence(hold.Evidence)); err != nil {
		t.Fatalf("RecordReceipt: %v", err)
	}
	allow, err := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "worker-api",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"pkg/cache/config.go"},
		Interfaces:        []string{"CacheConfig.Expiry"},
	})
	if err != nil {
		t.Fatalf("EvaluateAction retry: %v", err)
	}
	if allow.Decision != "allow" || !allow.ReceiptVerified {
		t.Fatalf("expected allow after receipt, got %+v", allow)
	}

	// Delegated child worker cannot inherit parent's receipt!
	childHold, err := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "worker-api-child",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"pkg/cache/config.go"},
		Interfaces:        []string{"CacheConfig.Expiry"},
	})
	if err != nil {
		t.Fatalf("EvaluateAction child: %v", err)
	}
	if childHold.Decision != "hold" || childHold.ReceiptVerified {
		t.Fatalf("parent receipt must not cover delegated child: %+v", childHold)
	}
}

func TestProposalLifecycleSupersessionNotifiesPriorRecipient(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()

	if _, err := st.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: "acme/widgets",
		ProposalKey:  "rfc/diag-v1",
		SourceType:   "rfc",
		SourceRef:    "docs/rfcs/diag.md",
		Title:        "Diagnostic encoding v1",
		State:        "draft",
		ScopePaths:   []string{"compiler/diag/**"},
		Visibility:   "public",
	}); err != nil {
		t.Fatal(err)
	}

	// Worker receives draft v1 and records receipt.
	first, _ := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "w1",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"compiler/diag/span.go"},
	})
	_, _ = eng.RecordReceipt(ctx, "acme/widgets", "w1", 1, coordination.ReceiptItemsFromEvidence(first.Evidence))

	// Proposal is superseded -> prior recipient w1 MUST receive a lifecycle update!
	if _, err := st.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: "acme/widgets",
		ProposalKey:  "rfc/diag-v1",
		SourceType:   "rfc",
		SourceRef:    "docs/rfcs/diag.md",
		Title:        "Diagnostic encoding v1",
		State:        "superseded",
		SupersededBy: "rfc/diag-v2",
		ScopePaths:   []string{"compiler/diag/**"},
		Visibility:   "public",
	}); err != nil {
		t.Fatal(err)
	}

	priorUpdate, _ := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "w1",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"compiler/diag/span.go"},
	})
	if priorUpdate.Decision != "hold" || len(priorUpdate.MissingOrStaleEvidence) != 1 || priorUpdate.MissingOrStaleEvidence[0].LifecycleState != "superseded" {
		t.Fatalf("expected supersession update for prior recipient, got %+v", priorUpdate)
	}

	// Brand new worker w2 who never received rfc/diag-v1 sees zero applicability and clears silently.
	freshWorker, _ := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName:      "acme/widgets",
		WorkerID:          "w2",
		ContextGeneration: 1,
		Operation:         "write",
		TargetPaths:       []string{"compiler/diag/span.go"},
	})
	if freshWorker.Decision != "allow" || !freshWorker.Silent {
		t.Fatalf("superseded proposal should not apply to new worker, got %+v", freshWorker)
	}
}

func TestOverlapDetectionSuppressesLockfilesAndHonorsAck(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()

	_ = st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: "acme/neura",
		Number:       318,
		Title:        "Suppress duplicate diagnostic in renderer (Refs #309)",
		State:        "open",
		Draft:        true,
		HeadSHA:      "1111111",
		BaseRef:      "main",
		LinkedIssues: coordination.ExtractLinkedIssues("Suppress duplicate diagnostic in renderer (Refs #309)"),
		Paths:        []string{"renderer/diag.go", "go.sum"},
		LastAction:   "opened",
	})
	_ = st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: "acme/neura",
		Number:       322,
		Title:        "Fix parser duplicate diagnostic emission (Fixes #309)",
		State:        "open",
		Draft:        true,
		HeadSHA:      "2222222",
		BaseRef:      "main",
		LinkedIssues: coordination.ExtractLinkedIssues("Fix parser duplicate diagnostic emission (Fixes #309)"),
		Paths:        []string{"parser/emit.go", "go.sum"},
		LastAction:   "opened",
	})
	// Unrelated PR that only touches go.sum must NOT trigger overlap.
	_ = st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: "acme/neura",
		Number:       325,
		Title:        "Bump sqlite dependency",
		State:        "open",
		HeadSHA:      "3333333",
		BaseRef:      "main",
		Paths:        []string{"go.sum"},
		LastAction:   "opened",
	})

	overlaps, err := eng.DetectOverlaps(ctx, "acme/neura", 0, false)
	if err != nil {
		t.Fatalf("DetectOverlaps: %v", err)
	}
	if len(overlaps) != 1 || overlaps[0].PRA != 318 || overlaps[0].PRB != 322 || overlaps[0].Source != "taaro" || overlaps[0].Authoritative {
		t.Fatalf("unexpected overlaps: %+v", overlaps)
	}

	// Acknowledge as complementary -> repeated alert suppressed.
	if _, err := eng.AcknowledgeOverlap(ctx, "acme/neura", 322, 318, "complementary", "alice", "Renderer + parser split"); err != nil {
		t.Fatalf("AcknowledgeOverlap: %v", err)
	}
	afterAck, err := eng.DetectOverlaps(ctx, "acme/neura", 0, false)
	if err != nil {
		t.Fatalf("DetectOverlaps after ack: %v", err)
	}
	if len(afterAck) != 0 {
		t.Fatalf("expected acknowledged overlap to be suppressed, got %+v", afterAck)
	}
}

func TestCheckReuseIdentityAndSpeculativeCancellation(t *testing.T) {
	eng, _ := newTestEngine(t)
	ctx := context.Background()

	// Start a speculative in-flight combined validation for (prod1, cons1).
	first, cancelled, err := eng.RecordValidationCheck(ctx, store.ValidationCheckRecord{
		RepoFullName: "acme/neura",
		JobName:      "compat-diag",
		PRNumber:     318,
		ProducerRev:  "prod1",
		ConsumerRev:  "cons1",
		InputsHash:   "in-aaa",
		Toolchain:    "go1.27.2",
		Flags:        "-race",
		Environment:  "linux-amd64",
		Status:       "in_progress",
	})
	if err != nil || len(cancelled) != 0 {
		t.Fatalf("first check: %v cancelled=%v", err, cancelled)
	}

	// Exact identity matches and is reusable.
	m1, err := eng.MatchEligibleCheck(ctx, coordination.MatchCheckRequest{
		RepoFullName: "acme/neura",
		JobName:      "compat-diag",
		ProducerRev:  "prod1",
		ConsumerRev:  "cons1",
		InputsHash:   "in-aaa",
		Toolchain:    "go1.27.2",
		Flags:        "-race",
		Environment:  "linux-amd64",
	})
	if err != nil || !m1.Reusable || m1.Check.ID != first.ID {
		t.Fatalf("expected reusable match, got %+v (%v)", m1, err)
	}

	// Different flags or unknown environment must NOT reuse.
	mDiff, _ := eng.MatchEligibleCheck(ctx, coordination.MatchCheckRequest{
		RepoFullName: "acme/neura",
		JobName:      "compat-diag",
		ProducerRev:  "prod1",
		ConsumerRev:  "cons1",
		InputsHash:   "in-aaa",
		Toolchain:    "go1.27.2",
		Flags:        "-tags=debug",
		Environment:  "linux-amd64",
	})
	if mDiff.Reusable {
		t.Fatalf("different flags must not reuse check: %+v", mDiff)
	}

	// Recording a new combined validation for (prod2, cons1) cancels the superseded speculative run!
	_, cancelled2, err := eng.RecordValidationCheck(ctx, store.ValidationCheckRecord{
		RepoFullName: "acme/neura",
		JobName:      "compat-diag",
		PRNumber:     318,
		ProducerRev:  "prod2",
		ConsumerRev:  "cons1",
		InputsHash:   "in-bbb",
		Toolchain:    "go1.27.2",
		Flags:        "-race",
		Environment:  "linux-amd64",
		Status:       "completed",
		Conclusion:   "success",
	})
	if err != nil || len(cancelled2) != 1 || cancelled2[0] != first.ID {
		t.Fatalf("expected superseded run %d to be cancelled, got %v (err=%v)", first.ID, cancelled2, err)
	}
}

func TestCandidateRequiresIndexedCurrentDiff(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()
	const repo = "acme/candidate"
	if err := st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: repo, Number: 1, HeadSHA: "current", State: "open", Paths: []string{"src/api.go"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo, ProposalKey: "api", ScopePaths: []string{"src/api.go"},
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		request coordination.CandidateAwarenessRequest
		verdict string
	}{
		{"old head", coordination.CandidateAwarenessRequest{PRNumber: 1, HeadSHA: "old", TargetPaths: []string{"README.md"}}, "in_progress"},
		{"unknown PR", coordination.CandidateAwarenessRequest{PRNumber: 2, HeadSHA: "current", TargetPaths: []string{"README.md"}}, "in_progress"},
		{"merge group without diff", coordination.CandidateAwarenessRequest{HeadSHA: "queue", RevisionKind: "merge_group"}, "in_progress"},
		{"caller cannot omit indexed scope", coordination.CandidateAwarenessRequest{PRNumber: 1, HeadSHA: "current", TargetPaths: []string{"README.md"}}, "action_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.RepoFullName = repo
			resp, err := eng.EvaluateCandidate(ctx, tc.request)
			if err != nil || resp.Evaluation.Verdict != tc.verdict || resp.Evaluation.Complete != (tc.verdict != "in_progress") {
				t.Fatalf("got %+v (%v), want %s", resp, err, tc.verdict)
			}
		})
	}
}

func TestEvidenceHashPreservesFieldBoundaries(t *testing.T) {
	first := []store.ReceiptItem{{Kind: "proposal", Key: "a|b", Version: "c", State: "draft"}}
	second := []store.ReceiptItem{{Kind: "proposal", Key: "a", Version: "b|c", State: "draft"}}
	if coordination.ComputeReceiptItemsHash(first) == coordination.ComputeReceiptItemsHash(second) {
		t.Fatal("different receipt identities produced the same hash")
	}
}

func TestScopeRevisionWithinSameSecondInvalidatesReceipt(t *testing.T) {
	eng, _ := newTestEngine(t)
	now := eng.Now()
	eng.Now = func() time.Time { return now }
	ctx := context.Background()
	scope := coordination.RegisterScopeRequest{
		RepoFullName: "acme/widgets", WorkerID: "peer", ContextGeneration: 1,
		Paths: []string{"src/api.go"},
	}
	if _, err := eng.RegisterScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	req := coordination.EvaluateActionRequest{
		RepoFullName: scope.RepoFullName, WorkerID: "actor", ContextGeneration: 1,
		Operation: "write", TargetPaths: scope.Paths,
	}
	resp, err := eng.EvaluateAction(ctx, req)
	if err != nil || resp.Decision != "hold" {
		t.Fatalf("initial evaluate = %+v, %v", resp, err)
	}
	if _, err := eng.RecordReceipt(ctx, req.RepoFullName, req.WorkerID, req.ContextGeneration, coordination.ReceiptItemsFromEvidence(resp.Evidence)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	if _, err := eng.RegisterScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	resp, err = eng.EvaluateAction(ctx, req)
	if err != nil || resp.Decision != "allow" {
		t.Fatalf("renewing identical scope invalidated its receipt: %+v, %v", resp, err)
	}

	now = now.Add(time.Millisecond)
	scope.BaseRev = "new-base"
	if _, err := eng.RegisterScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	resp, err = eng.EvaluateAction(ctx, req)
	if err != nil || resp.Decision != "hold" || len(resp.MissingOrStaleEvidence) != 1 {
		t.Fatalf("evaluate after peer revision = %+v, %v", resp, err)
	}
}

func TestScopeExpiryAtWholeSecond(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()
	expires := eng.Now().Add(time.Second)
	if _, err := eng.RegisterScope(ctx, coordination.RegisterScopeRequest{
		RepoFullName: "acme/widgets", WorkerID: "peer", ContextGeneration: 1,
		Paths: []string{"src/api.go"}, TTL: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListActiveScopes(ctx, "acme/widgets", expires.Add(-time.Nanosecond))
	if err != nil || len(before) != 1 {
		t.Fatalf("scope prematurely expired: %+v (%v)", before, err)
	}
	at, err := st.ListActiveScopes(ctx, "acme/widgets", expires)
	if err != nil || len(at) != 0 {
		t.Fatalf("scope did not expire at its deadline: %+v (%v)", at, err)
	}
	after, err := st.ListActiveScopes(ctx, "acme/widgets", expires.Add(time.Nanosecond))
	if err != nil || len(after) != 0 {
		t.Fatalf("expired scope reappeared after whole second: %+v (%v)", after, err)
	}
}

func TestHandoffRedactionAndRevisionSpecificAwareness(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()
	if err := st.UpsertPullRequest(ctx, store.PullRequestRecord{RepoFullName: "o/r", Number: 1, HeadSHA: "same", Paths: []string{"src/a"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.ProposalRecord{
		{RepoFullName: "o/r", ProposalKey: "secret1", Title: "private name", Visibility: "private", State: "draft", ScopePaths: []string{"src/a"}},
		{RepoFullName: "o/r", ProposalKey: "secret2", Title: "other private", Visibility: "internal", State: "accepted", ScopePaths: []string{"src/a"}},
		{RepoFullName: "o/r", ProposalKey: "public", Title: "draft", Visibility: "public", State: "draft", ScopePaths: []string{"src/a"}},
	} {
		if _, err := st.UpsertProposal(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []store.AwarenessEvaluation{{RepoFullName: "o/r", PRNumber: 1, HeadSHA: "same", RevisionKind: "pr_head", Verdict: "failure"}, {RepoFullName: "o/r", PRNumber: 2, HeadSHA: "same", RevisionKind: "pr_head", Verdict: "success"}} {
		if _, err := st.RecordAwarenessEvaluation(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	card, err := eng.AssembleHandoff(ctx, "o/r", 1, "public")
	if err != nil {
		t.Fatal(err)
	}
	if card.Awareness == nil || card.Awareness.PRNumber != 1 || card.Awareness.Verdict != "failure" {
		t.Fatalf("wrong awareness: %+v", card.Awareness)
	}
	if len(card.Proposals) != 2 {
		t.Fatalf("unexpected proposals: %+v", card.Proposals)
	}
	for _, p := range card.Proposals {
		if p.Redacted && (p.Key != "" || p.Title != "" || p.Version != "" || p.LifecycleState != "") {
			t.Fatalf("private metadata leaked: %+v", p)
		}
		if p.Key == "public" && p.Authority != "taaro_suggestion" {
			t.Fatalf("draft authority: %+v", p)
		}
	}
}

func TestHandoffMarkdownBoundsAndQuotesUntrustedText(t *testing.T) {
	hostile := "`\n@everyone [link](https://evil.invalid) " + strings.Repeat("x", 10000)
	card := coordination.HandoffCard{PullRequest: store.PullRequestRecord{Number: 1, Title: hostile, HeadSHA: "head"}, Artifacts: []store.ArtifactRecord{{ArtifactKey: hostile, Command: hostile}}, Proposals: []coordination.EvidenceItem{{Title: hostile, Key: hostile}}}
	out := coordination.RenderHandoffMarkdown(card)
	if strings.Contains(out, "`\n@everyone") || len(out) > 5000 {
		t.Fatalf("unsafe or oversized output: %q", out)
	}
}

type staleArtifactLookupStore struct {
	store.Store
}

func (s staleArtifactLookupStore) ListWorkerArtifactSubscriptions(context.Context, string, string, int) ([]store.ArtifactSubscription, error) {
	return []store.ArtifactSubscription{{ArtifactKey: "fixture", LatestVersion: 2, Stale: true}}, nil
}

func (s staleArtifactLookupStore) GetArtifact(context.Context, string, string, int) (store.ArtifactRecord, error) {
	return store.ArtifactRecord{}, errors.New("fixture lookup unavailable")
}

func TestUnavailableSubscribedArtifactLeavesProtectionPending(t *testing.T) {
	eng, st := newTestEngine(t)
	ctx := context.Background()
	const repo = "acme/widgets"
	if err := st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: repo, Number: 1, HeadSHA: "current", State: "open", Paths: []string{"src/api.go"},
	}); err != nil {
		t.Fatal(err)
	}
	eng.Store = staleArtifactLookupStore{Store: st}
	action, err := eng.EvaluateAction(ctx, coordination.EvaluateActionRequest{
		RepoFullName: repo, WorkerID: "worker", ContextGeneration: 1,
		Operation: "write", TargetPaths: []string{"src/api.go"},
	})
	if err != nil || action.Decision != "pending" {
		t.Fatalf("protected action = %+v (%v)", action, err)
	}
	candidate, err := eng.EvaluateCandidate(ctx, coordination.CandidateAwarenessRequest{
		RepoFullName: repo, PRNumber: 1, HeadSHA: "current", WorkerID: "worker", ContextGeneration: 1,
	})
	if err != nil || candidate.Evaluation.Verdict != "in_progress" || candidate.Evaluation.Complete {
		t.Fatalf("candidate = %+v (%v)", candidate, err)
	}
}

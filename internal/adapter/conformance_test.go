package adapter_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Kayyo321/taaro/internal/adapter"
	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
)

func startConformanceServer(t *testing.T) (*httptest.Server, *store.SQLiteStore, *adapter.Client) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "conformance.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := &server.Server{
		AppID:    1001,
		APIToken: "conformance-token",
		Store:    st,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cli := adapter.NewClient(ts.URL, "conformance-token")
	return ts, st, cli
}

// TestConformanceLevel2FixtureExchangeAndRevisionDelivery exercises the Neura compiler/editor
// single-repository exchange scenario from docs/PROJECT.md §2 and §9.
func TestConformanceLevel2FixtureExchangeAndRevisionDelivery(t *testing.T) {
	_, _, cli := startConformanceServer(t)
	ctx := context.Background()
	const repo = "acme/neura"

	// 1. Compiler worker publishes provisional golden fixture v1.
	pub1, err := cli.PublishArtifact(ctx, store.ArtifactRecord{
		RepoFullName:   repo,
		ArtifactKey:    "compiler/diagnostic-span",
		ProducerWorker: "compiler-w1",
		ProducerRev:    "comp-rev-1",
		PRNumber:       101,
		Command:        "go test ./editor/decoder -run TestDiagnosticSpan",
		ContentType:    "application/json",
		Payload:        `{"span":{"start":10,"end":24,"secondary":null}}`,
		Summary:        "Secondary diagnostic span fixture v1",
	})
	if err != nil {
		t.Fatalf("PublishArtifact v1: %v", err)
	}
	if pub1.Artifact.Version != 1 || len(pub1.StaleConsumers) != 0 {
		t.Fatalf("unexpected v1 publish: %+v", pub1)
	}

	// 2. Editor worker subscribes and consumes v1 on its checkout.
	editor := adapter.NewWorkerSession(cli, repo, "editor-w1", "editor")
	sub, err := cli.SubscribeArtifact(ctx, store.ArtifactSubscription{
		RepoFullName:      repo,
		ArtifactKey:       "compiler/diagnostic-span",
		WorkerID:          editor.WorkerID,
		ContextGeneration: editor.ContextGeneration,
		ConsumedVersion:   1,
		ConsumerRev:       "edit-rev-1",
	})
	if err != nil || sub.Stale {
		t.Fatalf("SubscribeArtifact v1: %+v (%v)", sub, err)
	}

	// 3. Compiler revises encoding for absent span -> immutable v2, editor-w1 flagged stale!
	pub2, err := cli.PublishArtifact(ctx, store.ArtifactRecord{
		RepoFullName:   repo,
		ArtifactKey:    "compiler/diagnostic-span",
		ProducerWorker: "compiler-w1",
		ProducerRev:    "comp-rev-2",
		PRNumber:       101,
		Command:        "go test ./editor/decoder -run TestDiagnosticSpan",
		ContentType:    "application/json",
		Payload:        `{"span":{"start":10,"end":24,"secondary":{"absent":true}}}`,
		Summary:        "Secondary diagnostic span fixture v2 (explicit absent marker)",
	})
	if err != nil {
		t.Fatalf("PublishArtifact v2: %v", err)
	}
	if pub2.Artifact.Version != 2 || len(pub2.StaleConsumers) != 1 || pub2.StaleConsumers[0].WorkerID != "editor-w1" {
		t.Fatalf("expected v2 with stale consumer editor-w1, got %+v", pub2)
	}

	// Verify v1 remains strictly immutable when retrieved by exact version!
	v1Again, err := cli.GetArtifact(ctx, repo, "compiler/diagnostic-span", 1)
	if err != nil || v1Again.Payload != pub1.Artifact.Payload {
		t.Fatalf("v1 artifact mutated or missing: %+v (%v)", v1Again, err)
	}

	// 4. Before editor-w1's next affected write, mediation holds and delivers v2!
	applied := false
	_, err = editor.MediatedWrite(ctx, "write", []string{"editor/decoder/span.go"}, nil, nil, func() error {
		applied = true
		return nil
	})
	if !errors.Is(err, adapter.ErrFreshTurnRequired) || applied {
		t.Fatalf("expected hold+fresh-turn for stale fixture consumer, applied=%v err=%v", applied, err)
	}

	// Editor updates consumption to v2 and retries on a fresh turn -> succeeds.
	_, _ = cli.SubscribeArtifact(ctx, store.ArtifactSubscription{
		RepoFullName:      repo,
		ArtifactKey:       "compiler/diagnostic-span",
		WorkerID:          editor.WorkerID,
		ContextGeneration: editor.ContextGeneration,
		ConsumedVersion:   2,
		ConsumerRev:       "edit-rev-2",
	})
	if _, err := editor.MediatedWrite(ctx, "write", []string{"editor/decoder/span.go"}, nil, nil, func() error {
		applied = true
		return nil
	}); err != nil || !applied {
		t.Fatalf("expected fresh turn write to succeed after consuming v2, applied=%v err=%v", applied, err)
	}

	// 5. Record combined compatibility validation identifying BOTH tested revisions.
	chk, err := cli.RecordCheck(ctx, store.ValidationCheckRecord{
		RepoFullName:    repo,
		JobName:         "compat-diagnostic-span",
		PRNumber:        101,
		ProducerRev:     "comp-rev-2",
		ConsumerRev:     "edit-rev-2",
		InputsHash:      "sha256:inputs-v2",
		Toolchain:       "go1.27.2",
		Flags:           "-race",
		Environment:     "linux-amd64",
		ArtifactKey:     "compiler/diagnostic-span",
		ArtifactVersion: 2,
		Status:          "completed",
		Conclusion:      "success",
	})
	if err != nil || chk.Check.ProducerRev != "comp-rev-2" || chk.Check.ConsumerRev != "edit-rev-2" {
		t.Fatalf("unexpected combined check: %+v (%v)", chk, err)
	}
}

// TestConformanceLevel3AwarenessTrial exercises every awareness scenario in docs/PROJECT.md §10:
// pre-existing draft, new revision on unchanged head, delegation, compaction/reset, scope expansion,
// simultaneous starts, private redaction, unavailable index, routine typo fix, and required check.
func TestConformanceLevel3AwarenessTrial(t *testing.T) {
	ts, st, cli := startConformanceServer(t)
	ctx := context.Background()
	const repo = "acme/core"

	// Pre-existing draft proposal.
	if _, err := cli.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo,
		ProposalKey:  "rfc/lexer-multiline",
		SourceType:   "rfc",
		SourceRef:    "docs/rfcs/lexer.md",
		Title:        "Multiline string spans point to opening quote",
		State:        "draft",
		ScopePaths:   []string{"internal/lexer/**"},
		Interfaces:   []string{"Lexer.ScanString"},
		Summary:      "Record opening quote offset.",
		Excerpt:      "Unterminated multiline strings report opening quote span.",
		Visibility:   "public",
	}); err != nil {
		t.Fatalf("UpsertProposal: %v", err)
	}

	// 1. Pre-existing draft holds write, delivers quoted data, discards edit, then fresh turn allows.
	parent := adapter.NewWorkerSession(cli, repo, "worker-parent", "implementer")
	editExecuted := false
	holdResp, err := parent.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		editExecuted = true
		return nil
	})
	if !errors.Is(err, adapter.ErrFreshTurnRequired) || editExecuted || !holdResp.DiscardPendingEdit {
		t.Fatalf("expected hold + discard on pre-existing draft: resp=%+v err=%v edited=%v", holdResp, err, editExecuted)
	}
	if _, err := parent.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		editExecuted = true
		return nil
	}); err != nil || !editExecuted {
		t.Fatalf("expected allow on fresh turn after receipt: err=%v edited=%v", err, editExecuted)
	}

	// 2. Delegation: parent receipt CANNOT cover delegated child worker!
	child := parent.Delegate("worker-child", "subtask")
	childEdited := false
	if _, err := child.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		childEdited = true
		return nil
	}); !errors.Is(err, adapter.ErrFreshTurnRequired) || childEdited {
		t.Fatalf("delegated child must not inherit parent receipt: err=%v edited=%v", err, childEdited)
	}

	// 3. New proposal revision on unchanged head invalidates parent's prior receipt!
	if _, err := cli.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo,
		ProposalKey:  "rfc/lexer-multiline",
		SourceType:   "rfc",
		SourceRef:    "docs/rfcs/lexer.md",
		Title:        "Multiline string spans point to opening quote",
		State:        "accepted",
		ScopePaths:   []string{"internal/lexer/**"},
		Interfaces:   []string{"Lexer.ScanString"},
		Summary:      "Record opening quote offset; handle trailing backslash at EOF.",
		Excerpt:      "Trailing backslash at EOF must not panic.",
		Visibility:   "public",
	}); err != nil {
		t.Fatalf("UpsertProposal v2: %v", err)
	}
	rev2Edited := false
	if _, err := parent.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		rev2Edited = true
		return nil
	}); !errors.Is(err, adapter.ErrFreshTurnRequired) || rev2Edited {
		t.Fatalf("proposal revision v2 must invalidate prior receipt: err=%v edited=%v", err, rev2Edited)
	}
	// Fresh turn succeeds after receiving v2.
	if _, err := parent.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		rev2Edited = true
		return nil
	}); err != nil || !rev2Edited {
		t.Fatalf("expected allow after v2 receipt: err=%v", err)
	}

	// 4. Compaction / reset drops context generation -> forces re-delivery.
	parent.CompactOrReset()
	compEdited := false
	if _, err := parent.MediatedWrite(ctx, "write", []string{"internal/lexer/string.go"}, nil, nil, func() error {
		compEdited = true
		return nil
	}); !errors.Is(err, adapter.ErrFreshTurnRequired) || compEdited {
		t.Fatalf("compaction must require fresh delivery in new generation: err=%v edited=%v", err, compEdited)
	}

	// 5. Scope expansion into a path with a private proposal under public audience -> review_required without leaking contents!
	if _, err := cli.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo,
		ProposalKey:  "rfc/private-billing",
		SourceType:   "rfc",
		SourceRef:    "private/billing.md",
		Title:        "Top Secret Pricing Model",
		State:        "accepted",
		ScopePaths:   []string{"internal/billing/**"},
		Excerpt:      "CONFIDENTIAL-PRICING-TABLE",
		Visibility:   "private",
	}); err != nil {
		t.Fatal(err)
	}
	pubWorker := adapter.NewWorkerSession(cli, repo, "worker-external", "contributor")
	pubWorker.Audience = "public"
	privResp, err := pubWorker.MediatedWrite(ctx, "write", []string{"internal/billing/rate.go"}, nil, nil, nil)
	if !errors.Is(err, adapter.ErrActionHeld) || privResp.Decision != "review_required" {
		t.Fatalf("expected review_required for private proposal, got resp=%+v err=%v", privResp, err)
	}
	for _, ev := range privResp.Evidence {
		if ev.Title != "" || ev.QuotedExcerpt != "" || !ev.Redacted {
			t.Fatalf("private proposal leaked content: %+v", ev)
		}
	}

	// 6. Simultaneous worker starts: two concurrent RegisterScope calls on the same path;
	// atomic transaction guarantees at least one sees the other (cannot both see empty!).
	var wg sync.WaitGroup
	var resA, resB coordination.RegisterScopeResponse
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		wa := adapter.NewWorkerSession(cli, repo, "simul-A", "worker")
		resA, errA = wa.RegisterScope(ctx, 0, "base1", []string{"internal/parser/expr.go"}, nil, []int{555})
	}()
	go func() {
		defer wg.Done()
		wb := adapter.NewWorkerSession(cli, repo, "simul-B", "worker")
		resB, errB = wb.RegisterScope(ctx, 0, "base1", []string{"internal/parser/expr.go"}, nil, []int{555})
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("simultaneous RegisterScope failed: errA=%v errB=%v", errA, errB)
	}
	if len(resA.Evidence) == 0 && len(resB.Evidence) == 0 {
		t.Fatalf("simultaneous starts BOTH observed empty scope; atomicity violated")
	}

	// 7. Unrelated routine typo fix requires zero holds or acknowledgments!
	typoWorker := adapter.NewWorkerSession(cli, repo, "worker-typo", "maintainer")
	typoApplied := false
	typoResp, err := typoWorker.MediatedWrite(ctx, "write", []string{"README.md"}, nil, nil, func() error {
		typoApplied = true
		return nil
	})
	if err != nil || !typoApplied || !typoResp.Silent {
		t.Fatalf("routine typo fix must clear silently: resp=%+v applied=%v err=%v", typoResp, typoApplied, err)
	}

	// 8. Required Awareness Check: uninstrumented PR touching lexer fails ("action_required"),
	// whereas PR with verified worker receipt succeeds ("success").
	if err := st.UpsertPullRequest(ctx, store.PullRequestRecord{
		RepoFullName: repo,
		Number:       247,
		HeadSHA:      "sha-candidate",
		State:        "open",
		Paths:        []string{"internal/lexer/string.go"},
	}); err != nil {
		t.Fatalf("index candidate: %v", err)
	}
	uninst, err := cli.VerifyCandidate(ctx, coordination.CandidateAwarenessRequest{
		RepoFullName: repo,
		PRNumber:     247,
		HeadSHA:      "sha-candidate",
	}, false)
	if err != nil || uninst.Evaluation.Verdict != "action_required" {
		t.Fatalf("expected action_required for uninstrumented matching PR, got %+v (%v)", uninst, err)
	}
	// parent has a receipt at gen 2 after step 4!
	verified, err := cli.VerifyCandidate(ctx, coordination.CandidateAwarenessRequest{
		RepoFullName:      repo,
		PRNumber:          247,
		HeadSHA:           "sha-candidate",
		WorkerID:          parent.WorkerID,
		ContextGeneration: parent.ContextGeneration,
	}, false)
	if err != nil || verified.Evaluation.Verdict != "success" || !verified.Evaluation.Complete {
		t.Fatalf("expected success with valid receipt, got %+v (%v)", verified, err)
	}

	// 9. Unavailable coordination index fails closed (ErrActionHeld, pending, edit never runs!).
	ts.Close()
	offlineEdited := false
	offlineResp, err := typoWorker.MediatedWrite(ctx, "write", []string{"README.md"}, nil, nil, func() error {
		offlineEdited = true
		return nil
	})
	if !errors.Is(err, adapter.ErrActionHeld) || offlineEdited || offlineResp.Decision != "pending" {
		t.Fatalf("unavailable index must fail closed: resp=%+v edited=%v err=%v", offlineResp, offlineEdited, err)
	}
}

func TestScopeRegistrationDeliversContextAndGuardsPrivateReads(t *testing.T) {
	_, _, cli := startConformanceServer(t)
	ctx := context.Background()
	const repo = "acme/core"
	if _, err := cli.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo, ProposalKey: "public-rfc", Title: "Public RFC", SourceType: "rfc", SourceRef: "docs/public.md",
		ScopePaths: []string{"src/public.go"}, Visibility: "public",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repo, ProposalKey: "private-rfc", Title: "Private RFC", SourceType: "rfc", SourceRef: "docs/private.md",
		ScopePaths: []string{"src/private.go"}, Visibility: "private",
	}); err != nil {
		t.Fatal(err)
	}

	w := adapter.NewWorkerSession(cli, repo, "actor", "worker")
	w.Audience = "public"
	if _, err := w.RegisterScope(ctx, 0, "base", []string{"src/public.go"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.DeliveredContext) != 1 || w.DeliveredContext[0].Key != "public-rfc" {
		t.Fatalf("scope evidence was not delivered: %+v", w.DeliveredContext)
	}
	resp, err := w.MediatedWrite(ctx, "write", []string{"src/public.go"}, nil, nil, nil)
	if err != nil || resp.Decision != "allow" || !resp.ReceiptVerified {
		t.Fatalf("write after scope delivery = %+v, %v", resp, err)
	}
	resp, err = w.GuardedRead(ctx, []string{"src/private.go"}, nil, nil)
	if !errors.Is(err, adapter.ErrActionHeld) || resp.Decision != "review_required" {
		t.Fatalf("private read = %+v, %v", resp, err)
	}
	if len(resp.Evidence) != 1 || !resp.Evidence[0].Redacted || resp.Evidence[0].Key != "" || resp.Evidence[0].Version != "" {
		t.Fatalf("private source details were exposed: %+v", resp.Evidence)
	}
}

package webhook_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

func TestProposalRefreshInvalidatesOnlyStaleAffectedChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taaro.db")
	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const repo = "acme/widgets"
	proposal := store.ProposalRecord{
		RepoFullName: repo, ProposalKey: "api", Title: "API proposal",
		ScopePaths: []string{"src/api.go"},
	}
	if _, err := st.UpsertProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	watermark, err := st.CurrentProposalAwarenessWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range []store.PullRequestRecord{
		{Number: 1, HeadSHA: "affected-before", Paths: []string{"src/api.go"}},
		{Number: 2, HeadSHA: "unrelated", Paths: []string{"README.md"}},
		{Number: 3, HeadSHA: "affected-after", Paths: []string{"src/api.go"}},
	} {
		pr.RepoFullName, pr.State, pr.InstallationID = repo, "open", 100
		if err := st.UpsertPullRequest(ctx, pr); err != nil {
			t.Fatal(err)
		}
		if pr.Number == 3 {
			eval, err := st.RecordAwarenessEvaluation(ctx, store.AwarenessEvaluation{
				RepoFullName: repo, PRNumber: pr.Number, HeadSHA: pr.HeadSHA, Verdict: "success",
				Complete: true, ProposalWatermark: watermark,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.RecordAwarenessPublication(ctx, eval, 103); err != nil {
				t.Fatal(err)
			}
		} else if err := st.RecordAwarenessCheckRun(ctx, repo, pr.Number, pr.HeadSHA, int64(100+pr.Number), "success"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	publisher := &fakeAwarenessPublisher{}
	h := &webhook.Handler{Store: st, AwarenessPublisher: publisher, EnableAwarenessCheck: true}
	h.Drain()
	h.Wait()
	if len(publisher.calls) != 1 {
		t.Fatalf("expected one queued invalidation, got %+v", publisher.calls)
	}
	call := publisher.calls[0]
	if call.PRNumber != 1 || call.HeadSHA != "affected-before" || call.ExistingCheckRun != 101 || call.Conclusion != "action_required" {
		t.Fatalf("unexpected invalidation: %+v", call)
	}

	// A removed selector still invalidates checks evaluated against the prior scope.
	proposal.ScopePaths = []string{"src/replacement.go"}
	if _, err := st.UpsertProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	h.Wait()
	if len(publisher.calls) != 2 || publisher.calls[1].PRNumber != 3 {
		t.Fatalf("expected old-scope check to be invalidated, got %+v", publisher.calls)
	}

	// An unchanged write is an observation of the existing version.
	if _, err := st.UpsertProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	latest, err := st.CurrentProposalAwarenessWatermark(ctx)
	if err != nil || latest != watermark+1 {
		t.Fatalf("unchanged proposal enqueued a refresh: watermark=%d, err=%v", latest, err)
	}
}

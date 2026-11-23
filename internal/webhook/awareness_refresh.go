package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
)

type proposalAwarenessQueue interface {
	NextProposalAwarenessRefresh(context.Context, time.Time) (store.ProposalAwarenessRefresh, error)
	FinishProposalAwarenessRefresh(context.Context, int64, time.Time) error
	AwarenessPublicationWatermark(context.Context, store.PullRequestRecord) (int64, error)
}

func (h *Handler) drainProposalAwarenessRefresh() {
	queue, ok := h.Store.(proposalAwarenessQueue)
	if !ok || h.AwarenessPublisher == nil {
		return
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		job, err := queue.NextProposalAwarenessRefresh(ctx, time.Now())
		if err != nil {
			cancel()
			if !errors.Is(err, store.ErrNotFound) {
				h.logError("cannot read proposal awareness refresh queue", "err", err)
			}
			return
		}

		err = h.refreshProposalAwareness(ctx, job, queue)
		cancel()
		var retryAt time.Time
		if err != nil {
			h.logError("proposal awareness refresh failed; retry scheduled", "repo", job.RepoFullName, "err", err)
			retryAt = time.Now().Add(5 * time.Second)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		err = queue.FinishProposalAwarenessRefresh(ctx, job.ID, retryAt)
		cancel()
		if err != nil {
			h.logError("cannot update proposal awareness refresh queue", "err", err)
			return
		}
	}
}

func (h *Handler) refreshProposalAwareness(ctx context.Context, job store.ProposalAwarenessRefresh, queue proposalAwarenessQueue) error {
	engine := h.engine()
	engine.AwarenessPublication.Lock()
	defer engine.AwarenessPublication.Unlock()

	prs, err := h.Store.ListOpenPullRequests(ctx, job.RepoFullName)
	if err != nil {
		return err
	}
	owner, repo, ok := strings.Cut(job.RepoFullName, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return nil
	}
	for _, pr := range prs {
		if pr.AwarenessConclusion != "success" || pr.AwarenessCheckHead != pr.HeadSHA ||
			pr.AwarenessCheckRunID <= 0 || pr.InstallationID <= 0 {
			continue
		}
		if !coordination.ProposalAffectsPullRequest(job.Previous, pr) && !coordination.ProposalAffectsPullRequest(job.Current, pr) {
			continue
		}

		watermark, err := queue.AwarenessPublicationWatermark(ctx, pr)
		if err != nil {
			return err
		}
		if watermark >= job.ID {
			continue
		}

		// The affected revision needs fresh delivery to its acting worker.
		// The queue has no worker context and can only revoke the old clearance.
		now := h.now()
		const reason = "Relevant proposal evidence changed. Evaluate the current candidate again before merging."
		checkID, err := h.AwarenessPublisher.PublishAwarenessCheck(ctx, githubapp.AwarenessCheckInput{
			InstallationID: pr.InstallationID, Owner: owner, Repo: repo, PRNumber: pr.Number,
			HeadSHA: pr.HeadSHA, RevisionKind: "pr_head", Status: "completed", Conclusion: "action_required",
			Title: "Awareness needs re-evaluation", SummaryMarkdown: reason,
			ExistingCheckRun: pr.AwarenessCheckRunID, ObservedAt: now,
		})
		if err != nil {
			return fmt.Errorf("invalidate awareness for PR %d: %w", pr.Number, err)
		}
		if _, err := h.Store.RecordAwarenessEvaluation(ctx, store.AwarenessEvaluation{
			RepoFullName: job.RepoFullName, PRNumber: pr.Number, HeadSHA: pr.HeadSHA,
			RevisionKind: "pr_head", Verdict: "action_required", Reason: reason,
			CheckRunID: checkID, IndexedAt: now, ValidatedAt: now,
		}); err != nil {
			return err
		}
		if err := h.Store.RecordAwarenessCheckRun(ctx, job.RepoFullName, pr.Number, pr.HeadSHA, checkID, "action_required"); err != nil {
			return err
		}
	}
	return nil
}

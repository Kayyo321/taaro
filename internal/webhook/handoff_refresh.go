package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
)

// Fixture publication and validation records originate outside GitHub webhooks.
func (h *Handler) drainHandoffRefresh() {
	q, ok := h.Store.(interface {
		NextHandoffRefresh(context.Context) (store.HandoffRefresh, error)
		FinishHandoffRefresh(context.Context, store.HandoffRefresh, bool) error
	})
	if !ok {
		return
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		item, err := q.NextHandoffRefresh(ctx)
		if err != nil {
			cancel()
			if !errors.Is(err, store.ErrNotFound) {
				h.logError("read handoff refresh queue", "err", err)
			}
			return
		}
		if h.Publisher != nil {
			var pr store.PullRequestRecord
			pr, err = h.Store.GetPullRequest(ctx, item.Repo, item.PR)
			if errors.Is(err, store.ErrNotFound) {
				err = nil
			} else if err == nil && pr.State == "open" {
				err = h.publishHandoffForPR(ctx, item.Repo, item.PR, pr.InstallationID, pr.HeadSHA, pr.CheckRunID, h.now())
			}
		}
		cancel()
		if err != nil {
			h.logError("handoff refresh failed", "repo", item.Repo, "pr", item.PR, "err", err)
		}
		if err = q.FinishHandoffRefresh(context.Background(), item, err == nil); err != nil {
			h.logError("finish handoff refresh", "err", err)
			return
		}
	}
}

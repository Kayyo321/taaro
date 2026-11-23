package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
)

const maxWebhookBodyBytes = 10 << 20 // 10 MiB

// Handler processes verified GitHub App webhook deliveries.
type issueReconciler interface {
	ReconcileIssue(context.Context, int64, string, string, int) (githubapp.IssueSnapshot, error)
}

type PullRequestReconciler interface {
	ReconcilePullRequest(context.Context, int64, string, string, int) (store.PullRequestRecord, bool, error)
}

type Handler struct {
	Reconciler           PullRequestReconciler
	prMu                 sync.Mutex
	AppID                int64
	WebhookSecret        string
	PublicURL            string
	DumpDir              string
	Store                store.Store
	Engine               *coordination.Engine
	Publisher            githubapp.HandoffPublisher
	AwarenessPublisher   githubapp.AwarenessPublisher
	EnableAwarenessCheck bool
	Logger               *slog.Logger
	Now                  func() time.Time

	wg       sync.WaitGroup
	queueMu  sync.Mutex
	draining bool
	wake     bool
}

// Wait blocks until all in-flight asynchronous webhook jobs finish.
func (h *Handler) Wait() {
	h.wg.Wait()
}

func (h *Handler) engine() *coordination.Engine {
	if h.Engine != nil {
		return h.Engine
	}
	return &coordination.Engine{Store: h.Store, Now: h.Now}
}

// ServeHTTP verifies the webhook signature, deduplicates by delivery ID,
// excludes self-originated check_run events, and acknowledges promptly with 202.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	sigHeader := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	if event == "" || deliveryID == "" {
		http.Error(w, "missing X-GitHub-Event or X-GitHub-Delivery header", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		http.Error(w, "failed reading request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxWebhookBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	if !VerifySignature(h.WebhookSecret, body, sigHeader) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}

	observedAt := h.now()

	if h.DumpDir != "" {
		if err := h.dumpDelivery(deliveryID, event, sigHeader, body, observedAt); err != nil && h.Logger != nil {
			h.Logger.Warn("failed dumping webhook delivery", "delivery_id", deliveryID, "err", err)
		}
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	// Exclude the App's own check_run events to prevent self-triggering refresh loops (docs/PROJECT.md §7).
	if event == "check_run" && h.AppID > 0 && env.CheckRun != nil && env.CheckRun.App.ID == h.AppID {
		h.logDebug("ignoring self-originated check_run event", "delivery_id", deliveryID, "app_id", h.AppID)
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ignored_self_check_run",
			"delivery_id": deliveryID,
		})
		return
	}

	isNew, err := h.Store.RecordDelivery(r.Context(), store.DeliveryRecord{
		DeliveryID:     deliveryID,
		Event:          event,
		Action:         env.Action,
		RepoFullName:   env.Repository.FullName,
		ReceivedAt:     observedAt,
		InstallationID: env.Installation.ID,
		Payload:        body,
	})
	if err != nil {
		h.logError("failed recording webhook delivery", "delivery_id", deliveryID, "err", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if !isNew {
		h.logDebug("ignoring duplicate webhook delivery", "delivery_id", deliveryID, "event", event)
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "duplicate",
			"delivery_id": deliveryID,
		})
		return
	}

	h.Drain()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":      "accepted",
		"delivery_id": deliveryID,
		"event":       event,
	})
}

func (h *Handler) processEvent(ctx context.Context, deliveryID, event string, body []byte, observedAt time.Time) error {
	switch event {
	case "pull_request":
		return h.handlePullRequest(ctx, deliveryID, body, observedAt)
	case "pull_request_review":
		return h.handlePullRequestReview(ctx, deliveryID, body, observedAt)
	case "issues":
		return h.handleIssue(ctx, deliveryID, body, observedAt)
	case "check_run":
		return h.handleExternalCheckRun(ctx, deliveryID, body, observedAt)
	case "merge_group":
		return h.handleMergeGroup(ctx, deliveryID, body, observedAt)
	case "installation":
		return h.handleInstallation(ctx, deliveryID, body)
	case "installation_repositories":
		return h.handleInstallationRepositories(ctx, deliveryID, body)
	case "ping":
		h.logInfo("received GitHub ping webhook", "delivery_id", deliveryID)
	default:
		h.logDebug("recorded unhandled webhook event", "delivery_id", deliveryID, "event", event)
	}
	return nil
}

func (h *Handler) handlePullRequest(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) error {
	var payload pullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding pull_request payload", "delivery_id", deliveryID, "err", err)
		return err
	}

	h.prMu.Lock()
	defer h.prMu.Unlock()
	repoFullName := payload.Repository.FullName
	prNum := payload.PullRequest.Number
	var reconciled *store.PullRequestRecord
	if h.Reconciler != nil {
		owner, repo, ok := strings.Cut(repoFullName, "/")
		if !ok || prNum <= 0 || payload.Installation.ID <= 0 {
			return fmt.Errorf("invalid pull request identity")
		}
		current, private, err := h.Reconciler.ReconcilePullRequest(ctx, payload.Installation.ID, owner, repo, prNum)
		if err != nil {
			return err
		}
		reconciled = &current
		payload.Repository.Private = private
		payload.PullRequest.Title = current.Title
		payload.PullRequest.Body = current.Body
		payload.PullRequest.State = current.State
		payload.PullRequest.Draft = current.Draft
		payload.PullRequest.Merged = current.Merged
		payload.PullRequest.MergeCommitSHA = current.MergeCommitSHA
		payload.PullRequest.HTMLURL = current.HTMLURL
		payload.PullRequest.User.Login = current.Author
		payload.PullRequest.Head.SHA = current.HeadSHA
		payload.PullRequest.Base.Ref = current.BaseRef
		payload.PullRequest.Labels = nil
		for _, label := range current.Labels {
			payload.PullRequest.Labels = append(payload.PullRequest.Labels, labelPayload{Name: label})
		}
	}
	headSHA := payload.PullRequest.Head.SHA

	var existingCheckRunID int64
	var existingAwarenessID int64
	prev, err := h.Store.GetPullRequest(ctx, repoFullName, prNum)
	if err == nil {
		if prev.CheckRunHead == headSHA && prev.CheckRunID > 0 {
			existingCheckRunID = prev.CheckRunID
		}
		if prev.AwarenessCheckHead == headSHA && prev.AwarenessCheckRunID > 0 {
			existingAwarenessID = prev.AwarenessCheckRunID
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		h.logWarn("failed reading prior PR record", "repo", repoFullName, "pr", prNum, "err", err)
		return err
	}

	labels := make([]string, 0, len(payload.PullRequest.Labels))
	for _, l := range payload.PullRequest.Labels {
		if name := strings.TrimSpace(l.Name); name != "" {
			labels = append(labels, name)
		}
	}
	linkedIssues := coordination.ExtractLinkedIssues(payload.PullRequest.Title, payload.PullRequest.Body)

	rec := store.PullRequestRecord{
		RepoFullName:   repoFullName,
		Number:         prNum,
		Title:          payload.PullRequest.Title,
		State:          payload.PullRequest.State,
		Draft:          payload.PullRequest.Draft,
		HeadSHA:        headSHA,
		BaseRef:        payload.PullRequest.Base.Ref,
		HTMLURL:        payload.PullRequest.HTMLURL,
		Author:         payload.PullRequest.User.Login,
		Body:           truncateText(payload.PullRequest.Body, 2000),
		Labels:         labels,
		LinkedIssues:   linkedIssues,
		Merged:         payload.PullRequest.Merged,
		MergeCommitSHA: payload.PullRequest.MergeCommitSHA,
		RevisionKind:   "pr_head",
		InstallationID: payload.Installation.ID,
		CheckRunID:     existingCheckRunID,
		CheckRunHead:   headSHA,
		LastAction:     payload.Action,
		ObservedAt:     observedAt,
	}
	if reconciled != nil {
		rec.Paths = reconciled.Paths
	}
	if err := h.Store.UpsertPullRequest(ctx, rec); err != nil {
		h.logError("failed upserting pull request", "repo", repoFullName, "pr", prNum, "err", err)
		return err
	}

	// If a PR carries a design/proposal label, also index it as a proposal source.
	if hasProposalLabel(labels) {
		vis := "public"
		if payload.Repository.Private {
			vis = "private"
		}
		state := inferProposalState(payload.PullRequest.State, payload.PullRequest.Draft, payload.PullRequest.Merged, labels)
		if _, err := h.Store.UpsertProposal(ctx, store.ProposalRecord{
			RepoFullName: repoFullName,
			ProposalKey:  fmt.Sprintf("pr/%d", prNum),
			SourceType:   "pr",
			SourceRef:    fmt.Sprintf("#%d", prNum),
			SourceURL:    payload.PullRequest.HTMLURL,
			Title:        payload.PullRequest.Title,
			Owner:        payload.PullRequest.User.Login,
			State:        state,
			LinkedIssues: linkedIssues,
			Summary:      payload.PullRequest.Title,
			Excerpt:      truncateText(payload.PullRequest.Body, 500),
			Visibility:   vis,
			UpdatedAt:    observedAt,
			ObservedAt:   observedAt,
		}); err != nil {
			return err
		}
	} else if err := h.withdrawProposal(ctx, repoFullName, fmt.Sprintf("pr/%d", prNum), observedAt); err != nil {
		return err
	}

	if !shouldPublishHandoff(payload.Action) {
		h.logDebug("skipping handoff check for pull_request action", "repo", repoFullName, "pr", prNum, "action", payload.Action)
		return nil
	}

	if err := h.publishHandoffForPR(ctx, repoFullName, prNum, payload.Installation.ID, headSHA, existingCheckRunID, observedAt); err != nil {
		return err
	}

	if h.EnableAwarenessCheck && h.AwarenessPublisher != nil && payload.Installation.ID > 0 && headSHA != "" {
		if err := h.publishAwarenessForCandidate(ctx, repoFullName, prNum, payload.Installation.ID, headSHA, "pr_head", existingAwarenessID, observedAt); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) handlePullRequestReview(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) error {
	var payload reviewPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding pull_request_review payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	repoFullName := payload.Repository.FullName
	prNum := payload.PullRequest.Number
	if repoFullName == "" || prNum <= 0 || payload.Review.ID == 0 {
		return nil
	}

	subAt, err := time.Parse(time.RFC3339, payload.Review.SubmittedAt)
	if err != nil {
		subAt = observedAt
	}
	if err := h.Store.UpsertReview(ctx, store.ReviewRecord{
		RepoFullName: repoFullName,
		PRNumber:     prNum,
		ReviewID:     payload.Review.ID,
		Reviewer:     payload.Review.User.Login,
		State:        strings.ToUpper(payload.Review.State),
		CommitSHA:    payload.Review.CommitID,
		BodyExcerpt:  truncateText(payload.Review.Body, 400),
		HTMLURL:      payload.Review.HTMLURL,
		SubmittedAt:  subAt,
		ObservedAt:   observedAt,
	}); err != nil {
		h.logError("failed upserting pull request review", "repo", repoFullName, "pr", prNum, "err", err)
		return err
	}

	// Refresh existing handoff card for the PR's current head SHA so reviewers see the latest review state.
	pr, err := h.Store.GetPullRequest(ctx, repoFullName, prNum)
	if err == nil && pr.HeadSHA != "" {
		instID := payload.Installation.ID
		if instID == 0 {
			instID = pr.InstallationID
		}
		return h.publishHandoffForPR(ctx, repoFullName, prNum, instID, pr.HeadSHA, pr.CheckRunID, observedAt)
	}
	return nil
}

func (h *Handler) handleIssue(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) error {
	var payload issuePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding issues payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	repoFullName := payload.Repository.FullName
	if repoFullName == "" || payload.Issue.Number <= 0 {
		return nil
	}

	h.prMu.Lock()
	defer h.prMu.Unlock()
	if reconciler, ok := h.Reconciler.(issueReconciler); ok {
		owner, repo, valid := strings.Cut(repoFullName, "/")
		if !valid || payload.Installation.ID <= 0 {
			return fmt.Errorf("invalid issue identity")
		}
		current, err := reconciler.ReconcileIssue(ctx, payload.Installation.ID, owner, repo, payload.Issue.Number)
		if err != nil {
			return err
		}
		payload.Repository.Private = current.Private
		payload.Issue.Title, payload.Issue.Body, payload.Issue.State = current.Title, current.Body, current.State
		payload.Issue.User.Login, payload.Issue.HTMLURL = current.Owner, current.URL
		payload.Issue.Labels = nil
		for _, label := range current.Labels {
			payload.Issue.Labels = append(payload.Issue.Labels, labelPayload{Name: label})
		}
	}
	labels := make([]string, 0, len(payload.Issue.Labels))
	for _, l := range payload.Issue.Labels {
		if name := strings.TrimSpace(l.Name); name != "" {
			labels = append(labels, name)
		}
	}
	if !hasProposalLabel(labels) {
		return h.withdrawProposal(ctx, repoFullName, fmt.Sprintf("issue/%d", payload.Issue.Number), observedAt)
	}

	vis := "public"
	if payload.Repository.Private {
		vis = "private"
	}
	state := inferProposalState(payload.Issue.State, false, false, labels)
	if _, err := h.Store.UpsertProposal(ctx, store.ProposalRecord{
		RepoFullName: repoFullName,
		ProposalKey:  fmt.Sprintf("issue/%d", payload.Issue.Number),
		SourceType:   "issue",
		SourceRef:    fmt.Sprintf("#%d", payload.Issue.Number),
		SourceURL:    payload.Issue.HTMLURL,
		Title:        payload.Issue.Title,
		Owner:        payload.Issue.User.Login,
		State:        state,
		LinkedIssues: []int{payload.Issue.Number},
		Summary:      payload.Issue.Title,
		Excerpt:      truncateText(payload.Issue.Body, 500),
		Visibility:   vis,
		UpdatedAt:    observedAt,
		ObservedAt:   observedAt,
	}); err != nil {
		h.logError("failed indexing proposal issue", "repo", repoFullName, "issue", payload.Issue.Number, "err", err)
		return err
	}
	h.logInfo("indexed proposal from GitHub issue", "repo", repoFullName, "issue", payload.Issue.Number, "state", state)
	return nil
}

// A removed proposal label makes the indexed source inactive, with a new version.
func (h *Handler) withdrawProposal(ctx context.Context, repo, key string, observedAt time.Time) error {
	proposals, err := h.Store.ListProposals(ctx, repo)
	if err != nil {
		return err
	}
	for _, proposal := range proposals {
		if proposal.ProposalKey == key && proposal.State != "withdrawn" {
			proposal.State = "withdrawn"
			proposal.UpdatedAt = observedAt
			proposal.ObservedAt = observedAt
			_, err = h.Store.UpsertProposal(ctx, proposal)
			return err
		}
	}
	return nil
}

func (h *Handler) handleExternalCheckRun(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) error {
	var payload checkRunPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding check_run payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	repoFullName := payload.Repository.FullName
	if repoFullName == "" || payload.CheckRun.HeadSHA == "" || payload.CheckRun.ID == 0 {
		return nil
	}
	if err := h.Store.UpsertExternalCheckRun(ctx, store.ExternalCheckRun{
		RepoFullName: repoFullName,
		HeadSHA:      payload.CheckRun.HeadSHA,
		CheckRunID:   payload.CheckRun.ID,
		Name:         payload.CheckRun.Name,
		Status:       payload.CheckRun.Status,
		Conclusion:   payload.CheckRun.Conclusion,
		DetailsURL:   payload.CheckRun.DetailsURL,
		AppID:        payload.CheckRun.App.ID,
		ObservedAt:   observedAt,
	}); err != nil {
		h.logError("failed recording external check run", "repo", repoFullName, "check_run", payload.CheckRun.ID, "err", err)
		return err
	}
	return nil
}

func (h *Handler) handleMergeGroup(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) error {
	if !h.EnableAwarenessCheck {
		return nil
	}
	var payload mergeGroupPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding merge_group payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	repoFullName := payload.Repository.FullName
	headSHA := payload.MergeGroup.HeadSHA
	if repoFullName == "" || headSHA == "" {
		return nil
	}
	if payload.Action != "checks_requested" {
		h.logDebug("ignoring merge_group action", "repo", repoFullName, "action", payload.Action)
		return nil
	}
	return h.publishAwarenessForCandidate(ctx, repoFullName, 0, payload.Installation.ID, headSHA, "merge_group", 0, observedAt)
}

func (h *Handler) handleInstallation(ctx context.Context, deliveryID string, body []byte) error {
	var payload installationPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding installation payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	removing := payload.Action == "deleted" || payload.Action == "suspend"
	names := make(map[string]bool)
	if removing && payload.Installation.ID > 0 {
		known, err := h.Store.ListInstallationRepositories(ctx, payload.Installation.ID)
		if err != nil {
			return err
		}
		for _, name := range known {
			names[name] = true
		}
	}
	for _, repo := range payload.Repositories {
		if repo.FullName != "" {
			names[repo.FullName] = true
		}
	}
	for name := range names {
		if removing {
			if err := h.Store.PurgeRepository(ctx, name); err != nil {
				return err
			}
		} else if payload.Installation.ID > 0 {
			if err := h.Store.RegisterInstallationRepository(ctx, payload.Installation.ID, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) handleInstallationRepositories(ctx context.Context, deliveryID string, body []byte) error {
	var payload installationReposPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding installation_repositories payload", "delivery_id", deliveryID, "err", err)
		return err
	}
	for _, repo := range payload.RepositoriesAdded {
		if repo.FullName != "" && payload.Installation.ID > 0 {
			if err := h.Store.RegisterInstallationRepository(ctx, payload.Installation.ID, repo.FullName); err != nil {
				return err
			}
		}
	}
	for _, repo := range payload.RepositoriesRemoved {
		if repo.FullName == "" {
			continue
		}
		if err := h.Store.PurgeRepository(ctx, repo.FullName); err != nil {
			h.logError("failed purging removed repository", "repo", repo.FullName, "err", err)
			return err
		} else {
			h.logInfo("purged repository data after installation_repositories removal", "repo", repo.FullName)
		}
	}
	return nil
}

func (h *Handler) publishHandoffForPR(ctx context.Context, repoFullName string, prNum int, installationID int64, headSHA string, existingCheckRunID int64, observedAt time.Time) error {
	if h.Publisher == nil {
		h.logWarn("no GitHub publisher configured; skipping handoff Check Run", "repo", repoFullName, "pr", prNum)
		return nil
	}
	if installationID == 0 || headSHA == "" {
		h.logWarn("missing installation ID or head SHA; skipping handoff Check Run", "repo", repoFullName, "pr", prNum)
		return nil
	}

	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok {
		h.logWarn("invalid repository full_name", "repo", repoFullName)
		return nil
	}

	shortSHA := headSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	detailsURL := ""
	if h.PublicURL != "" {
		detailsURL = fmt.Sprintf("%s/handoffs/%s/%s/pull/%d?sha=%s", strings.TrimRight(h.PublicURL, "/"), owner, repo, prNum, url.QueryEscape(headSHA))
	}

	// Never publish a card assembled for a different revision, or conceal a failed lookup.
	card, err := h.engine().AssembleHandoff(ctx, repoFullName, prNum, "public")
	if err != nil {
		h.logWarn("failed assembling handoff", "repo", repoFullName, "pr", prNum, "err", err)
		return err
	}
	if card.PullRequest.HeadSHA != headSHA || !card.Actionable {
		return nil
	}
	summary := card.SummaryMarkdown

	checkRunID, err := h.Publisher.PublishHandoffCheck(ctx, githubapp.HandoffCheckInput{
		InstallationID:   installationID,
		Owner:            owner,
		Repo:             repo,
		PRNumber:         prNum,
		HeadSHA:          headSHA,
		Title:            fmt.Sprintf("Handoff snapshot at %s", shortSHA),
		SummaryMarkdown:  summary,
		DetailsURL:       detailsURL,
		ExistingCheckRun: existingCheckRunID,
		ObservedAt:       observedAt,
	})
	if err != nil {
		h.logError("failed publishing handoff check run", "repo", repoFullName, "pr", prNum, "head_sha", headSHA, "err", err)
		return err
	}

	if err := h.Store.RecordHandoffCheckRun(ctx, repoFullName, prNum, headSHA, checkRunID); err != nil {
		h.logWarn("failed recording handoff check run ID", "repo", repoFullName, "pr", prNum, "check_run_id", checkRunID, "err", err)
		return err
	}
	h.logInfo("published neutral handoff check run", "repo", repoFullName, "pr", prNum, "head_sha", shortSHA, "check_run_id", checkRunID)
	return nil
}

func (h *Handler) publishAwarenessForCandidate(ctx context.Context, repoFullName string, prNum int, installationID int64, headSHA, revisionKind string, existingCheckRunID int64, observedAt time.Time) error {
	engine := h.engine()
	engine.AwarenessPublication.Lock()
	defer engine.AwarenessPublication.Unlock()
	resp, err := engine.EvaluateCandidate(ctx, coordination.CandidateAwarenessRequest{
		RepoFullName: repoFullName,
		PRNumber:     prNum,
		HeadSHA:      headSHA,
		RevisionKind: revisionKind,
		Audience:     "public",
	})
	if err != nil {
		h.logError("failed evaluating awareness candidate", "repo", repoFullName, "head_sha", headSHA, "err", err)
		return err
	}
	if h.AwarenessPublisher == nil || installationID == 0 {
		return nil
	}

	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok {
		return nil
	}

	status := "completed"
	conclusion := resp.Evaluation.Verdict
	if conclusion == "in_progress" {
		status = "in_progress"
		conclusion = ""
	}

	shortSHA := headSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	detailsURL := ""
	if h.PublicURL != "" && prNum > 0 {
		detailsURL = fmt.Sprintf("%s/handoffs/%s/%s/pull/%d?sha=%s", strings.TrimRight(h.PublicURL, "/"), owner, repo, prNum, url.QueryEscape(headSHA))
	}

	summary := fmt.Sprintf(
		"**Taaro Awareness Check** on `%s` (`%s`)\n\n- **Complete:** `%t`\n- **Verdict:** `%s`\n- **Evidence hash:** `%s`\n- **Validated at:** `%s`\n- **Reason:** %s\n",
		shortSHA,
		revisionKind,
		resp.Evaluation.Complete,
		resp.Evaluation.Verdict,
		resp.Evaluation.EvidenceSetHash,
		resp.Evaluation.ValidatedAt.UTC().Format(time.RFC3339),
		resp.Evaluation.Reason,
	)

	checkRunID, err := h.AwarenessPublisher.PublishAwarenessCheck(ctx, githubapp.AwarenessCheckInput{
		InstallationID:   installationID,
		Owner:            owner,
		Repo:             repo,
		PRNumber:         prNum,
		HeadSHA:          headSHA,
		RevisionKind:     revisionKind,
		Status:           status,
		Conclusion:       conclusion,
		Title:            fmt.Sprintf("Awareness (%s): %s", revisionKind, resp.Evaluation.Verdict),
		SummaryMarkdown:  summary,
		DetailsURL:       detailsURL,
		ExistingCheckRun: existingCheckRunID,
		ObservedAt:       observedAt,
	})
	if err != nil {
		h.logError("failed publishing awareness check run", "repo", repoFullName, "head_sha", headSHA, "err", err)
		return err
	}
	if prNum > 0 {
		if err := engine.RecordAwarenessPublication(ctx, resp.Evaluation, checkRunID); err != nil {
			return err
		}
	}
	h.logInfo("published required awareness check run", "repo", repoFullName, "head_sha", shortSHA, "revision_kind", revisionKind, "verdict", resp.Evaluation.Verdict, "check_run_id", checkRunID)
	return nil
}

func shouldPublishHandoff(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "edited", "ready_for_review", "converted_to_draft":
		return true
	default:
		return false
	}
}

func hasProposalLabel(labels []string) bool {
	for _, l := range labels {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "proposal", "rfc", "design", "adr":
			return true
		}
	}
	return false
}

func inferProposalState(ghState string, draft, merged bool, labels []string) string {
	for _, l := range labels {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "superseded":
			return "superseded"
		case "withdrawn", "rejected":
			return "withdrawn"
		case "accepted", "approved":
			return "accepted"
		}
	}
	if merged {
		return "accepted"
	}
	if strings.EqualFold(ghState, "closed") {
		return "withdrawn"
	}
	if draft {
		return "draft"
	}
	return "draft"
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// VerifySignature verifies GitHub's X-Hub-Signature-256 HMAC-SHA256 header against raw payload bytes.
func VerifySignature(secret string, payload []byte, signatureHeader string) bool {
	if secret == "" || !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	gotHex := strings.TrimPrefix(signatureHeader, "sha256=")
	gotMAC, err := hex.DecodeString(gotHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)
	return hmac.Equal(gotMAC, expectedMAC)
}

// SignPayload generates a valid X-Hub-Signature-256 header value (useful for tests and local replay).
func SignPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) dumpDelivery(deliveryID, event, sig string, body []byte, ts time.Time) error {
	if err := os.MkdirAll(h.DumpDir, 0o700); err != nil {
		return err
	}
	safePart := func(value string) string {
		return strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return '_'
		}, value)
	}
	filename := fmt.Sprintf("%s_%s_%s.json", ts.UTC().Format("20060102T150405Z"), safePart(event), safePart(deliveryID))

	record := struct {
		ReceivedAt string          `json:"received_at"`
		DeliveryID string          `json:"delivery_id"`
		Event      string          `json:"event"`
		Signature  string          `json:"signature"`
		Payload    json.RawMessage `json:"payload"`
	}{
		ReceivedAt: ts.UTC().Format(time.RFC3339Nano),
		DeliveryID: deliveryID,
		Event:      event,
		Signature:  sig,
		Payload:    json.RawMessage(body),
	}
	out, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.DumpDir, filename), out, 0o600)
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func (h *Handler) logDebug(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Debug(msg, args...)
	}
}

func (h *Handler) logInfo(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Info(msg, args...)
	}
}

func (h *Handler) logWarn(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Warn(msg, args...)
	}
}

func (h *Handler) logError(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Error(msg, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type envelope struct {
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	CheckRun *struct {
		App struct {
			ID int64 `json:"id"`
		} `json:"app"`
	} `json:"check_run"`
}

type labelPayload struct {
	Name string `json:"name"`
}

type pullRequestPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	} `json:"repository"`
	PullRequest struct {
		Number         int            `json:"number"`
		Title          string         `json:"title"`
		Body           string         `json:"body"`
		State          string         `json:"state"`
		Draft          bool           `json:"draft"`
		Merged         bool           `json:"merged"`
		MergeCommitSHA string         `json:"merge_commit_sha"`
		HTMLURL        string         `json:"html_url"`
		Labels         []labelPayload `json:"labels"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

type reviewPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Review struct {
		ID          int64  `json:"id"`
		State       string `json:"state"`
		Body        string `json:"body"`
		CommitID    string `json:"commit_id"`
		HTMLURL     string `json:"html_url"`
		SubmittedAt string `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
}

type issuePayload struct {
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	} `json:"repository"`
	Issue struct {
		Number  int            `json:"number"`
		Title   string         `json:"title"`
		Body    string         `json:"body"`
		State   string         `json:"state"`
		HTMLURL string         `json:"html_url"`
		Labels  []labelPayload `json:"labels"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
}

type checkRunPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	CheckRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	} `json:"check_run"`
}

type mergeGroupPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	MergeGroup struct {
		HeadSHA string `json:"head_sha"`
		BaseRef string `json:"base_ref"`
	} `json:"merge_group"`
}

type installationPayload struct {
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Action       string `json:"action"`
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

type installationReposPayload struct {
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	RepositoriesAdded []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_added"`
	Action              string `json:"action"`
	RepositoriesRemoved []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_removed"`
}

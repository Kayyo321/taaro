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

	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
)

const maxWebhookBodyBytes = 10 << 20 // 10 MiB

// Handler processes verified GitHub App webhook deliveries.
type Handler struct {
	AppID         int64
	WebhookSecret string
	PublicURL     string
	DumpDir       string
	Store         store.Store
	Publisher     githubapp.HandoffPublisher
	Logger        *slog.Logger
	Now           func() time.Time

	wg sync.WaitGroup
}

// Wait blocks until all in-flight asynchronous webhook jobs finish.
func (h *Handler) Wait() {
	h.wg.Wait()
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
		DeliveryID:   deliveryID,
		Event:        event,
		Action:       env.Action,
		RepoFullName: env.Repository.FullName,
		ReceivedAt:   observedAt,
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

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h.processEvent(ctx, deliveryID, event, body, observedAt)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":      "accepted",
		"delivery_id": deliveryID,
		"event":       event,
	})
}

func (h *Handler) processEvent(ctx context.Context, deliveryID, event string, body []byte, observedAt time.Time) {
	switch event {
	case "pull_request":
		h.handlePullRequest(ctx, deliveryID, body, observedAt)
	case "ping":
		h.logInfo("received GitHub ping webhook", "delivery_id", deliveryID)
	default:
		h.logDebug("recorded unhandled webhook event", "delivery_id", deliveryID, "event", event)
	}
}

func (h *Handler) handlePullRequest(ctx context.Context, deliveryID string, body []byte, observedAt time.Time) {
	var payload pullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logError("failed decoding pull_request payload", "delivery_id", deliveryID, "err", err)
		return
	}

	repoFullName := payload.Repository.FullName
	prNum := payload.PullRequest.Number
	headSHA := payload.PullRequest.Head.SHA

	var existingCheckRunID int64
	prev, err := h.Store.GetPullRequest(ctx, repoFullName, prNum)
	if err == nil && prev.CheckRunHead == headSHA && prev.CheckRunID > 0 {
		existingCheckRunID = prev.CheckRunID
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logWarn("failed reading prior PR record", "repo", repoFullName, "pr", prNum, "err", err)
	}

	rec := store.PullRequestRecord{
		RepoFullName:   repoFullName,
		Number:         prNum,
		Title:          payload.PullRequest.Title,
		State:          payload.PullRequest.State,
		Draft:          payload.PullRequest.Draft,
		HeadSHA:        headSHA,
		BaseRef:        payload.PullRequest.Base.Ref,
		HTMLURL:        payload.PullRequest.HTMLURL,
		InstallationID: payload.Installation.ID,
		CheckRunID:     existingCheckRunID,
		CheckRunHead:   headSHA,
		LastAction:     payload.Action,
		ObservedAt:     observedAt,
	}
	if err := h.Store.UpsertPullRequest(ctx, rec); err != nil {
		h.logError("failed upserting pull request", "repo", repoFullName, "pr", prNum, "err", err)
		return
	}

	if !shouldPublishHandoff(payload.Action) {
		h.logDebug("skipping handoff check for pull_request action", "repo", repoFullName, "pr", prNum, "action", payload.Action)
		return
	}
	if h.Publisher == nil {
		h.logWarn("no GitHub publisher configured; skipping handoff Check Run", "repo", repoFullName, "pr", prNum)
		return
	}
	if payload.Installation.ID == 0 || headSHA == "" {
		h.logWarn("missing installation ID or head SHA; skipping handoff Check Run", "repo", repoFullName, "pr", prNum)
		return
	}

	owner, repo, ok := strings.Cut(repoFullName, "/")
	if !ok {
		h.logWarn("invalid repository full_name", "repo", repoFullName)
		return
	}

	shortSHA := headSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	detailsURL := ""
	if h.PublicURL != "" {
		detailsURL = fmt.Sprintf("%s/handoffs/%s/%s/pull/%d?sha=%s", strings.TrimRight(h.PublicURL, "/"), owner, repo, prNum, url.QueryEscape(headSHA))
	}

	summary := fmt.Sprintf(
		"**PR #%d** (`%s`) observed at `%s` via `%s`.\n\n- **Base branch:** `%s`\n- **Head SHA:** `%s`\n- **Draft:** `%t`\n- **CLI query:** `taaro context --repo %s --pr %d`\n",
		prNum,
		payload.PullRequest.Title,
		observedAt.UTC().Format(time.RFC3339),
		payload.Action,
		payload.PullRequest.Base.Ref,
		shortSHA,
		payload.PullRequest.Draft,
		repoFullName,
		prNum,
	)

	checkRunID, err := h.Publisher.PublishHandoffCheck(ctx, githubapp.HandoffCheckInput{
		InstallationID:   payload.Installation.ID,
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
		return
	}

	if err := h.Store.RecordHandoffCheckRun(ctx, repoFullName, prNum, headSHA, checkRunID); err != nil {
		h.logWarn("failed recording handoff check run ID", "repo", repoFullName, "pr", prNum, "check_run_id", checkRunID, "err", err)
	}
	h.logInfo("published neutral handoff check run", "repo", repoFullName, "pr", prNum, "head_sha", shortSHA, "check_run_id", checkRunID)
}

func shouldPublishHandoff(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "edited", "ready_for_review", "converted_to_draft":
		return true
	default:
		return false
	}
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
	if err := os.MkdirAll(h.DumpDir, 0o755); err != nil {
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

type pullRequestPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

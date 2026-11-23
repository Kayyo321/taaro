package webhook_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

type fakePublisher struct {
	mu    sync.Mutex
	calls []githubapp.HandoffCheckInput
	next  int64
}

func (f *fakePublisher) PublishHandoffCheck(_ context.Context, in githubapp.HandoffCheckInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if in.ExistingCheckRun > 0 {
		return in.ExistingCheckRun, nil
	}
	f.next++
	return 5000 + f.next, nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakePublisher) last() githubapp.HandoffCheckInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func newSignedRequest(t *testing.T, secret, event, deliveryID string, payload []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", webhook.SignPayload(secret, payload))
	return req
}

func TestWebhookPullRequestHandoffAndDeduplication(t *testing.T) {
	const secret = "test-webhook-secret"
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	dumpDir := filepath.Join(t.TempDir(), "dumps")
	pub := &fakePublisher{}
	fixedTime := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC)

	h := &webhook.Handler{
		AppID:         424242,
		WebhookSecret: secret,
		PublicURL:     "https://taaro.example.test",
		DumpDir:       dumpDir,
		Store:         st,
		Publisher:     pub,
		Now:           func() time.Time { return fixedTime },
	}

	if err := st.UpsertReview(context.Background(), store.ReviewRecord{RepoFullName: "Kayyo321/taaro", PRNumber: 247, ReviewID: 1, State: "CHANGES_REQUESTED", CommitSHA: "91bd80a1234567890abcdef1234567890abcdef1"}); err != nil {
		t.Fatal(err)
	}
	payload := loadFixture(t, "pull_request_opened.json")

	// 1. First delivery should be accepted asynchronously and publish one handoff check run.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "pull_request", "deliv-001", payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", rec.Code, rec.Body.String())
	}
	h.Wait()

	if pub.count() != 1 {
		t.Fatalf("expected 1 check run publish, got %d", pub.count())
	}
	call := pub.last()
	if call.Owner != "Kayyo321" || call.Repo != "taaro" || call.PRNumber != 247 {
		t.Fatalf("unexpected publish target: %+v", call)
	}
	if call.HeadSHA != "91bd80a1234567890abcdef1234567890abcdef1" {
		t.Fatalf("unexpected head SHA: %s", call.HeadSHA)
	}
	if call.DetailsURL != "https://taaro.example.test/handoffs/Kayyo321/taaro/pull/247?sha=91bd80a1234567890abcdef1234567890abcdef1" {
		t.Fatalf("unexpected details URL: %s", call.DetailsURL)
	}

	pr, err := st.GetPullRequest(context.Background(), "Kayyo321/taaro", 247)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if pr.CheckRunID != 5001 || !pr.ObservedAt.Equal(fixedTime) {
		t.Fatalf("unexpected stored PR: %+v", pr)
	}

	// Verify TAARO_DUMP_WEBHOOKS wrote the captured payload.
	entries, err := os.ReadDir(dumpDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 dumped webhook file, got %d (err=%v)", len(entries), err)
	}

	// 2. Duplicate delivery ID must return 200 OK and NOT publish a second Check Run.
	recDup := httptest.NewRecorder()
	h.ServeHTTP(recDup, newSignedRequest(t, secret, "pull_request", "deliv-001", payload))
	if recDup.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for duplicate delivery, got %d", recDup.Code)
	}
	h.Wait()
	if pub.count() != 1 {
		t.Fatalf("duplicate delivery should not republish check run, count=%d", pub.count())
	}

	// 3. New delivery on the SAME head SHA should update the existing Check Run ID.
	recSameHead := httptest.NewRecorder()
	h.ServeHTTP(recSameHead, newSignedRequest(t, secret, "pull_request", "deliv-002", payload))
	if recSameHead.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", recSameHead.Code)
	}
	h.Wait()
	if pub.count() != 2 || pub.last().ExistingCheckRun != 5001 {
		t.Fatalf("expected update of existing check run 5001, got count=%d existing=%d", pub.count(), pub.last().ExistingCheckRun)
	}
	if _, _, err := st.PublishArtifact(context.Background(), store.ArtifactRecord{RepoFullName: "Kayyo321/taaro", ArtifactKey: "fixture", PRNumber: 247, ProducerRev: "abc"}); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	h.Wait()
	if pub.count() != 3 || pub.last().ExistingCheckRun != 5001 || !strings.Contains(pub.last().SummaryMarkdown, "fixture") {
		t.Fatal("fixture publication did not refresh the existing check")
	}
	changed := bytes.ReplaceAll(payload, []byte(call.HeadSHA), []byte("abcdef1234567890abcdef1234567890abcdef12"))
	recChanged := httptest.NewRecorder()
	h.ServeHTTP(recChanged, newSignedRequest(t, secret, "pull_request", "deliv-003", changed))
	h.Wait()
	if recChanged.Code != http.StatusAccepted || pub.count() != 4 || pub.last().ExistingCheckRun != 0 {
		t.Fatal("new head should create a separate check run")
	}

}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	h := &webhook.Handler{
		AppID:         424242,
		WebhookSecret: "real-secret",
		Store:         st,
	}

	payload := loadFixture(t, "pull_request_opened.json")
	req := newSignedRequest(t, "wrong-secret", "pull_request", "deliv-bad-sig", payload)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestWebhookIgnoresSelfCheckRun(t *testing.T) {
	const secret = "test-webhook-secret"
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	h := &webhook.Handler{
		AppID:         424242,
		WebhookSecret: secret,
		Store:         st,
	}

	payload := loadFixture(t, "check_run_self.json")
	req := newSignedRequest(t, secret, "check_run", "deliv-self-cr", payload)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for ignored self check_run, got %d: %s", rec.Code, rec.Body.String())
	}
	h.Wait()
}

type fakeAwarenessPublisher struct {
	mu    sync.Mutex
	calls []githubapp.AwarenessCheckInput
}

func (f *fakeAwarenessPublisher) PublishAwarenessCheck(_ context.Context, in githubapp.AwarenessCheckInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return int64(8000 + len(f.calls)), nil
}

func TestWebhookReviewsProposalsExternalChecksMergeGroupAndPurge(t *testing.T) {
	const secret = "test-webhook-secret"
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	pub := &fakePublisher{}
	awPub := &fakeAwarenessPublisher{}
	h := &webhook.Handler{
		AppID:                424242,
		WebhookSecret:        secret,
		Store:                st,
		Publisher:            pub,
		AwarenessPublisher:   awPub,
		EnableAwarenessCheck: true,
	}

	// 1. Open PR #247.
	prPayload := loadFixture(t, "pull_request_opened.json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "pull_request", "deliv-pr-1", prPayload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	h.Wait()

	// 2. Submit a pull_request_review -> recorded and refreshes handoff card.
	reviewBody := []byte(`{
		"action": "submitted",
		"installation": {"id": 987654},
		"repository": {"full_name": "Kayyo321/taaro"},
		"pull_request": {"number": 247},
		"review": {
			"id": 701,
			"state": "changes_requested",
			"body": "Check trailing backslash at EOF.",
			"commit_id": "91bd80a1234567890abcdef1234567890abcdef1",
			"html_url": "https://github.com/Kayyo321/taaro/pull/247#pullrequestreview-701",
			"submitted_at": "2026-11-22T19:05:00Z",
			"user": {"login": "reviewer-1"}
		}
	}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "pull_request_review", "deliv-rev-1", reviewBody))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for review, got %d", rec.Code)
	}
	h.Wait()
	revs, err := st.ListReviews(context.Background(), "Kayyo321/taaro", 247)
	if err != nil || len(revs) != 1 || revs[0].State != "CHANGES_REQUESTED" {
		t.Fatalf("expected stored review, got %+v (%v)", revs, err)
	}

	// 3. Index a labeled proposal issue.
	issueBody := []byte(`{
		"action": "labeled",
		"repository": {"full_name": "Kayyo321/taaro", "private": false},
		"issue": {
			"number": 231,
			"title": "Multiline unterminated string points to opening quote",
			"body": "Diagnostic span must anchor to opening quote.",
			"state": "open",
			"html_url": "https://github.com/Kayyo321/taaro/issues/231",
			"labels": [{"name": "proposal"}, {"name": "accepted"}],
			"user": {"login": "maintainer"}
		}
	}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "issues", "deliv-iss-1", issueBody))
	h.Wait()
	props, err := st.ListProposals(context.Background(), "Kayyo321/taaro")
	if err != nil || len(props) != 1 || props[0].State != "accepted" || props[0].ProposalKey != "issue/231" {
		t.Fatalf("expected indexed accepted proposal, got %+v (%v)", props, err)
	}

	// 4. External (non-self) check_run is recorded against its head SHA.
	extCR := []byte(`{
		"action": "completed",
		"repository": {"full_name": "Kayyo321/taaro"},
		"check_run": {
			"id": 3333,
			"name": "diagnostics-test",
			"head_sha": "91bd80a1234567890abcdef1234567890abcdef1",
			"status": "completed",
			"conclusion": "success",
			"details_url": "https://github.com/Kayyo321/taaro/runs/3333",
			"app": {"id": 15368}
		}
	}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "check_run", "deliv-ext-cr", extCR))
	h.Wait()
	crs, err := st.ListExternalCheckRuns(context.Background(), "Kayyo321/taaro", "91bd80a1234567890abcdef1234567890abcdef1")
	if err != nil || len(crs) != 1 || crs[0].Name != "diagnostics-test" {
		t.Fatalf("expected recorded external check run, got %+v (%v)", crs, err)
	}

	// 5. Merge group event evaluates synthetic merge_group SHA.
	mgBody := []byte(`{
		"action": "checks_requested",
		"installation": {"id": 987654},
		"repository": {"full_name": "Kayyo321/taaro"},
		"merge_group": {
			"head_sha": "mg99999999999999999999999999999999999999",
			"base_ref": "refs/heads/main"
		}
	}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "merge_group", "deliv-mg-1", mgBody))
	h.Wait()
	awPub.mu.Lock()
	mgFound := false
	for _, c := range awPub.calls {
		if c.RevisionKind == "merge_group" && c.HeadSHA == "mg99999999999999999999999999999999999999" && c.Status == "in_progress" && c.Conclusion == "" {
			mgFound = true
		}
	}
	awPub.mu.Unlock()
	if !mgFound {
		t.Fatalf("expected merge_group awareness check publish, calls=%+v", awPub.calls)
	}

	// 6. Repository removal purges stored repository data promptly (docs/PROJECT.md §7).
	remBody := []byte(`{
		"action": "removed",
		"repositories_removed": [{"full_name": "Kayyo321/taaro"}]
	}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newSignedRequest(t, secret, "installation_repositories", "deliv-rem-1", remBody))
	h.Wait()
	if _, err := st.GetPullRequest(context.Background(), "Kayyo321/taaro", 247); err != store.ErrNotFound {
		t.Fatalf("expected repository data to be purged, got err=%v", err)
	}
}

func TestRemovedProposalLabelWithdrawsIndexedIssue(t *testing.T) {
	const secret = "secret"
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := &webhook.Handler{WebhookSecret: secret, Store: st}
	for i, labels := range []string{`[{"name":"proposal"}]`, `[]`} {
		body := []byte(fmt.Sprintf(`{"action":"edited","repository":{"full_name":"o/r"},"issue":{"number":3,"state":"open","title":"Plan","labels":%s}}`, labels))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newSignedRequest(t, secret, "issues", fmt.Sprintf("d%d", i), body))
		h.Wait()
		if w.Code != http.StatusAccepted {
			t.Fatalf("status %d", w.Code)
		}
	}
	proposals, err := st.ListProposals(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || proposals[0].State != "withdrawn" || proposals[0].Version != 2 {
		t.Fatalf("proposals %+v", proposals)
	}
}

package webhook_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	changed := bytes.ReplaceAll(payload, []byte(call.HeadSHA), []byte("abcdef1234567890abcdef1234567890abcdef12"))
	recChanged := httptest.NewRecorder()
	h.ServeHTTP(recChanged, newSignedRequest(t, secret, "pull_request", "deliv-003", changed))
	h.Wait()
	if recChanged.Code != http.StatusAccepted || pub.count() != 3 || pub.last().ExistingCheckRun != 0 {
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

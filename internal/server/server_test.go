package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

func localGet(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	req.RemoteAddr = "127.0.0.1:1234"
	return req
}

func TestServerHealthAndContextAPI(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	observed := time.Date(2026, 11, 22, 19, 15, 0, 0, time.UTC)
	if err := st.UpsertPullRequest(context.Background(), store.PullRequestRecord{
		RepoFullName: "Kayyo321/taaro",
		Number:       247,
		Title:        "Fix multiline span",
		State:        "open",
		Draft:        true,
		HeadSHA:      "91bd80a",
		BaseRef:      "main",
		HTMLURL:      "https://github.com/Kayyo321/taaro/pull/247",
		LastAction:   "opened",
		ObservedAt:   observed,
	}); err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}

	srv := &server.Server{
		AppID: 424242,
		Store: st,
		WebhookHandler: &webhook.Handler{
			AppID:         424242,
			WebhookSecret: "secret",
			Store:         st,
		},
	}

	// /healthz
	healthRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(healthRec, localGet("/healthz"))
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", healthRec.Code)
	}

	// /api/v1/context
	ctxRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ctxRec, localGet("/api/v1/context?repo=Kayyo321/taaro&pr=247"))
	if ctxRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/v1/context, got %d: %s", ctxRec.Code, ctxRec.Body.String())
	}

	var got store.PullRequestRecord
	if err := json.Unmarshal(ctxRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode context JSON: %v", err)
	}
	if got.Number != 247 || got.HeadSHA != "91bd80a" || !got.ObservedAt.Equal(observed) {
		t.Fatalf("unexpected context response: %+v", got)
	}

	// /handoffs/{owner}/{repo}/pull/{number}
	pageRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(pageRec, localGet("/handoffs/Kayyo321/taaro/pull/247"))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on handoff page, got %d", pageRec.Code)
	}

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, localGet("/handoffs/Kayyo321/taaro/pull/247?sha=old"))
	if stale.Code != http.StatusGone {
		t.Fatal("old link displayed current revision")
	}
}

func TestServerAPITokenAuthAndRedaction(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "taaro.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	_, err = st.UpsertProposal(context.Background(), store.ProposalRecord{
		RepoFullName: "acme/secret",
		ProposalKey:  "rfc/private-auth",
		SourceType:   "rfc",
		SourceRef:    "docs/private.md",
		Title:        "Confidential Auth Redesign",
		State:        "accepted",
		ScopePaths:   []string{"internal/auth/**"},
		Excerpt:      "SECRET-KEY-ROTATION-DETAIL",
		Visibility:   "private",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &server.Server{
		AppID:    1,
		APIToken: "tok-123",
		Store:    st,
	}
	h := srv.Handler()

	// Unauthenticated request to API must be rejected with 401.
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, localGet("/api/v1/proposals?repo=acme/secret"))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without Bearer token, got %d", unauth.Code)
	}

	// Authenticated request with audience=public must redact private proposal details!
	req := localGet("/api/v1/proposals?repo=acme/secret&audience=public")
	req.Header.Set("Authorization", "Bearer tok-123")
	pubRec := httptest.NewRecorder()
	h.ServeHTTP(pubRec, req)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", pubRec.Code, pubRec.Body.String())
	}
	if body := pubRec.Body.String(); len(body) == 0 || containsAny(body, "Confidential Auth Redesign", "SECRET-KEY-ROTATION-DETAIL", "internal/auth") {
		t.Fatalf("private proposal leaked to public audience: %s", body)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func TestRepositoryDataRequiresTrustedConnection(t *testing.T) {
	for _, path := range []string{"/api/v1/context?repo=a/b&pr=1", "/handoffs/a/b/pull/1"} {
		for _, tc := range []struct {
			token, host, peer, forward string
			status                     int
		}{
			{"secret", "127.0.0.1", "127.0.0.1:1", "", 401},
			{"", "attacker.example", "127.0.0.1:1", "", 403},
			{"", "127.0.0.1", "192.0.2.2:1", "", 403},
			{"", "127.0.0.1", "127.0.0.1:1", "198.51.100.1", 403},
		} {
			req := httptest.NewRequest("GET", path, nil)
			req.Host, req.RemoteAddr = tc.host, tc.peer
			req.Header.Set("X-Forwarded-For", tc.forward)
			w := httptest.NewRecorder()
			(&server.Server{APIToken: tc.token}).Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Errorf("%s: got %d, want %d", path, w.Code, tc.status)
			}
		}
	}
}

type rejectingPublisher struct{ calls int }

func (p *rejectingPublisher) PublishAwarenessCheck(context.Context, githubapp.AwarenessCheckInput) (int64, error) {
	p.calls++
	return 0, errors.New("mock GitHub refusal")
}
func TestVerifyCandidateReportsPublicationFailure(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.UpsertPullRequest(context.Background(), store.PullRequestRecord{RepoFullName: "a/b", Number: 1, State: "open", HeadSHA: "abc", InstallationID: 12, Paths: []string{"src/main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher := new(rejectingPublisher)
	srv := &server.Server{Store: st, APIToken: "token", AwarenessPublisher: publisher}
	for _, tc := range []struct {
		sha  string
		want int
	}{{"prior", 409}, {"abc", 502}} {
		req := httptest.NewRequest("POST", "/api/v1/awareness/verify-pr", strings.NewReader(fmt.Sprintf(`{"repo":"a/b","pr_number":1,"head_sha":%q,"publish":true}`, tc.sha)))
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: got %d %s", tc.sha, w.Code, w.Body.String())
		}
	}
	if publisher.calls != 1 {
		t.Fatalf("got %d publication attempts", publisher.calls)
	}
}

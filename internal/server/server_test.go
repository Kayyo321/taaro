package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

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
	srv.Handler().ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", healthRec.Code)
	}

	// /api/v1/context
	ctxRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ctxRec, httptest.NewRequest(http.MethodGet, "/api/v1/context?repo=Kayyo321/taaro&pr=247", nil))
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
	srv.Handler().ServeHTTP(pageRec, httptest.NewRequest(http.MethodGet, "/handoffs/Kayyo321/taaro/pull/247", nil))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on handoff page, got %d", pageRec.Code)
	}

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/handoffs/Kayyo321/taaro/pull/247?sha=old", nil))
	if stale.Code != http.StatusGone {
		t.Fatal("old link displayed current revision")
	}
}

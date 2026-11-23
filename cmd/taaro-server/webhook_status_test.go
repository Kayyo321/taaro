package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
	"github.com/google/go-github/v88/github"
)

func TestInspectWebhooksLocalAndUpstream(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "taaro.db")
	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.RecordDelivery(ctx, store.DeliveryRecord{DeliveryID: "known", Event: "pull_request", RepoFullName: "o/r", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.FinishDelivery(ctx, "known", false); err != nil {
		t.Fatal(err)
	}
	var recovered atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/app/hook/deliveries" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 400)
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("Link", `<https://api.github.com/app/hook/deliveries?cursor=abc>; rel="next"`)
			if recovered.Load() {
				_, _ = w.Write([]byte(`[{"id":5,"guid":"missing","status_code":202,"delivered_at":"2026-11-22T13:00:00Z"},{"id":3,"guid":"resolved","event":"issues","status":"OK","status_code":202,"delivered_at":"2026-11-22T12:00:00Z"},{"id":1,"guid":"known","status_code":202,"delivered_at":"2026-11-22T12:00:00Z"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":3,"guid":"resolved","event":"issues","status":"OK","status_code":202,"delivered_at":"2026-11-22T12:00:00Z"},{"id":1,"guid":"known","status_code":202,"delivered_at":"2026-11-22T12:00:00Z"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":2,"guid":"resolved","status_code":500,"delivered_at":"2026-11-22T11:00:00Z"},{"id":4,"guid":"missing","status_code":500,"delivered_at":"2026-11-22T10:00:00Z"}]`))
	}))
	defer api.Close()
	base := api.URL + "/"
	cli, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	report, err := inspectWebhooks(ctx, path, cli, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.PendingCount != 1 || len(report.Pending) != 1 || report.Pending[0].Attempts != 1 || len(report.Upstream) != 4 || !report.Upstream[1].LocalRecord || report.Upstream[3].LocalRecord || report.OutstandingFailed != 1 || report.UpstreamLimited {
		t.Fatalf("report: %+v", report)
	}
	if webhookAlertError(report) == nil {
		t.Fatal("expected alert")
	}
	if err := st.FinishDelivery(ctx, "known", true); err != nil {
		t.Fatal(err)
	}
	recovered.Store(true)
	report, err = inspectWebhooks(ctx, path, cli, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
	if err != nil || report.OutstandingFailed != 0 || report.RetryingCount != 0 {
		t.Fatalf("recovery report %+v: %v", report, err)
	}
	if err := webhookAlertError(report); err != nil {
		t.Fatal(err)
	}
}

func TestInspectWebhooksDoesNotCreateMissingDatabase(t *testing.T) {
	_, err := inspectWebhooks(context.Background(), filepath.Join(t.TempDir(), "missing.db"), nil, time.Now())
	if err == nil {
		t.Fatal("expected missing database error")
	}
}

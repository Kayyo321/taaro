package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v88/github"
)

func TestPublishHandoffCheckRequests(t *testing.T) {
	for _, tc := range []struct {
		name         string
		existing     int64
		updateStatus int
		wantCreate   bool
		wantError    bool
	}{
		{"first check", 0, 0, true, false},
		{"refresh check", 42, 200, false, false},
		{"replace missing check", 42, 404, true, false},
		{"failed update", 42, 500, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["conclusion"] != "neutral" || body["name"] != HandoffCheckName {
					t.Errorf("unexpected payload %v", body)
				}
				switch r.Method + " " + r.URL.Path {
				case "POST /repos/org/repo/check-runs":
					created = true
					if body["head_sha"] != "abc" {
						t.Error("missing target SHA")
					}
					w.WriteHeader(http.StatusCreated)
				case "PATCH /repos/org/repo/check-runs/42":
					if tc.updateStatus == 0 {
						t.Error("unexpected update")
						return
					}
					w.WriteHeader(tc.updateStatus)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
				_, _ = w.Write([]byte(`{"id":42}`))
			}))
			defer api.Close()
			base := api.URL + "/"
			cli, err := github.NewClient(github.WithURLs(&base, &base))
			if err != nil {
				t.Fatal(err)
			}
			c := &Client{installCache: map[int64]*github.Client{1: cli}}
			id, err := c.PublishHandoffCheck(context.Background(), HandoffCheckInput{
				InstallationID: 1, Owner: "org", Repo: "repo", HeadSHA: "abc", ExistingCheckRun: tc.existing,
			})
			if created != tc.wantCreate || (err != nil) != tc.wantError || (err == nil && id != 42) {
				t.Fatalf("created=%v id=%d err=%v", created, id, err)
			}
		})
	}
}

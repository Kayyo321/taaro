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

func TestPublishAwarenessCheckRejectsNeutralAndSkipped(t *testing.T) {
	c := &Client{installCache: map[int64]*github.Client{}}
	for _, forbidden := range []string{"neutral", "skipped"} {
		_, err := c.PublishAwarenessCheck(context.Background(), AwarenessCheckInput{
			InstallationID: 1,
			Owner:          "org",
			Repo:           "repo",
			HeadSHA:        "abc1234",
			Status:         "completed",
			Conclusion:     forbidden,
		})
		if err == nil {
			t.Fatalf("expected error for forbidden required-check conclusion %q", forbidden)
		}
	}
}

func TestPublishAwarenessCheckValidStates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		conclusion string
	}{
		{"success", "completed", "success"},
		{"action_required", "completed", "action_required"},
		{"in_progress_pending", "in_progress", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["name"] != AwarenessCheckName || body["status"] != tc.status {
					t.Errorf("unexpected payload %v", body)
				}
				if tc.conclusion == "" {
					if _, ok := body["conclusion"]; ok {
						t.Errorf("in_progress check must not set conclusion: %v", body)
					}
				} else if body["conclusion"] != tc.conclusion {
					t.Errorf("expected conclusion %q, got %v", tc.conclusion, body["conclusion"])
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":99}`))
			}))
			defer api.Close()
			base := api.URL + "/"
			cli, err := github.NewClient(github.WithURLs(&base, &base))
			if err != nil {
				t.Fatal(err)
			}
			c := &Client{installCache: map[int64]*github.Client{1: cli}}
			id, err := c.PublishAwarenessCheck(context.Background(), AwarenessCheckInput{
				InstallationID: 1,
				Owner:          "org",
				Repo:           "repo",
				PRNumber:       10,
				HeadSHA:        "deadbeef",
				RevisionKind:   "merge_group",
				Status:         tc.status,
				Conclusion:     tc.conclusion,
				Title:          "Awareness check",
			})
			if err != nil || id != 99 {
				t.Fatalf("id=%d err=%v", id, err)
			}
		})
	}
}

func TestReconcilePullRequestRejectsRacesAndIncompleteFiles(t *testing.T) {
	for _, tc := range []struct {
		name             string
		changed          int
		race             bool
		wantNil, wantErr bool
	}{
		{"complete renamed file", 1, false, false, false},
		{"incomplete listing", 2, false, true, false},
		{"file cap", 3000, false, true, false},
		{"head changed", 1, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/org/repo/pulls/1/files" {
					_, _ = w.Write([]byte(`[{"filename":"new.go","previous_filename":"old.go"}]`))
					return
				}
				calls++
				sha := "abc"
				if tc.race && calls > 1 {
					sha = "def"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 1, "changed_files": tc.changed, "head": map[string]any{"sha": sha}, "base": map[string]any{"sha": "base", "ref": "main", "repo": map[string]any{"private": false}}})
			}))
			defer api.Close()
			base := api.URL + "/"
			cli, err := github.NewClient(github.WithURLs(&base, &base))
			if err != nil {
				t.Fatal(err)
			}
			c := &Client{installCache: map[int64]*github.Client{1: cli}}
			pr, _, err := c.ReconcilePullRequest(context.Background(), 1, "org", "repo", 1)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			if err == nil && (pr.Paths == nil) != tc.wantNil {
				t.Fatalf("paths: %v", pr.Paths)
			}
			if err == nil && !tc.wantNil && (len(pr.Paths) != 2 || pr.Paths[1] != "old.go") {
				t.Fatalf("renamed paths: %v", pr.Paths)
			}
		})
	}
}

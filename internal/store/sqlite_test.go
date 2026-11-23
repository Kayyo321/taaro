package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteDeliveryDeduplication(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	rec := DeliveryRecord{
		DeliveryID:   "deliv-123",
		Event:        "pull_request",
		Action:       "opened",
		RepoFullName: "Kayyo321/taaro",
		ReceivedAt:   time.Now().UTC(),
	}

	isNew, err := st.RecordDelivery(ctx, rec)
	if err != nil {
		t.Fatalf("first RecordDelivery: %v", err)
	}
	if !isNew {
		t.Fatalf("expected first delivery to be new")
	}

	isNew, err = st.RecordDelivery(ctx, rec)
	if err != nil {
		t.Fatalf("second RecordDelivery: %v", err)
	}
	if isNew {
		t.Fatalf("expected duplicate delivery to be ignored")
	}
}

func TestSQLitePullRequestUpsertAndLookup(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.GetPullRequest(ctx, "Kayyo321/taaro", 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	observed := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	pr := PullRequestRecord{
		RepoFullName:   "Kayyo321/taaro",
		Number:         42,
		Title:          "Add handoff check",
		State:          "open",
		Draft:          true,
		HeadSHA:        "abc1234",
		BaseRef:        "main",
		HTMLURL:        "https://github.com/Kayyo321/taaro/pull/42",
		InstallationID: 99,
		LastAction:     "opened",
		ObservedAt:     observed,
	}
	if err := st.UpsertPullRequest(ctx, pr); err != nil {
		t.Fatalf("UpsertPullRequest: %v", err)
	}
	if err := st.RecordHandoffCheckRun(ctx, pr.RepoFullName, pr.Number, pr.HeadSHA, 777); err != nil {
		t.Fatalf("RecordHandoffCheckRun: %v", err)
	}

	got, err := st.GetPullRequest(ctx, pr.RepoFullName, pr.Number)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if got.HeadSHA != "abc1234" || got.CheckRunID != 777 || !got.Draft || !got.ObservedAt.Equal(observed) {
		t.Fatalf("unexpected PR record: %+v", got)
	}
}

func TestLateCheckDoesNotReplaceCurrentHeadCheck(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	rec := PullRequestRecord{RepoFullName: "example/repo", Number: 1}
	for id, sha := range []string{"first", "second"} {
		rec.HeadSHA = sha
		if err := st.UpsertPullRequest(ctx, rec); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetPullRequest(ctx, rec.RepoFullName, 1)
		if err != nil || got.CheckRunID != 0 {
			t.Fatalf("stale check: %+v (%v)", got, err)
		}
		if err := st.RecordHandoffCheckRun(ctx, rec.RepoFullName, 1, sha, int64(id+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordHandoffCheckRun(ctx, rec.RepoFullName, 1, "first", 1); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPullRequest(ctx, rec.RepoFullName, 1)
	if err != nil || got.CheckRunHead != "second" || got.CheckRunID != 2 {
		t.Fatalf("wrong current check: %+v (%v)", got, err)
	}
}

func TestPullRequestClearsRemovedFieldsAndStalePaths(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	pr := PullRequestRecord{RepoFullName: "o/r", Number: 1, HeadSHA: "one", Body: "old", Labels: []string{"proposal"}, LinkedIssues: []int{7}, Paths: []string{"old.go"}}
	if err := st.UpsertPullRequest(ctx, pr); err != nil {
		t.Fatal(err)
	}
	pr.HeadSHA = "two"
	pr.Body = ""
	pr.Labels = []string{}
	pr.LinkedIssues = []int{}
	pr.Paths = nil
	if err := st.UpsertPullRequest(ctx, pr); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPullRequest(ctx, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "" || len(got.Labels) != 0 || len(got.LinkedIssues) != 0 || got.Paths != nil {
		t.Fatalf("stale fields: %+v", got)
	}
}

func TestPendingWebhookSurvivesRestartAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d := DeliveryRecord{DeliveryID: "guid", Event: "issues", RepoFullName: "a/b", InstallationID: 123, Payload: []byte("{}")}
	if _, err := s.RecordDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	next, err := s.NextDelivery(ctx, now)
	if err != nil || next.DeliveryID != d.DeliveryID || string(next.Payload) != "{}" {
		t.Fatalf("restart: %+v %v", next, err)
	}
	if err := s.FinishDelivery(ctx, d.DeliveryID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextDelivery(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry backoff: %v", err)
	}
	if _, err := s.NextDelivery(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	repos, err := s.ListInstallationRepositories(ctx, 123)
	if err != nil || len(repos) != 1 || repos[0] != "a/b" {
		t.Fatalf("installation map: %v %v", repos, err)
	}
	if err := s.PurgeRepository(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextDelivery(ctx, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged payload: %v", err)
	}
}

func TestSubscriptionReturnsPersistedConsumption(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	sub := ArtifactSubscription{RepoFullName: "a/b", ArtifactKey: "fixture", WorkerID: "w", ContextGeneration: 1, ConsumedVersion: 1, ConsumerRev: "abc"}
	if _, err = s.UpsertArtifactSubscription(ctx, sub); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("accepted unpublished version: %v", err)
	}
	if _, _, err = s.PublishArtifact(ctx, ArtifactRecord{RepoFullName: "a/b", ArtifactKey: "fixture", ProducerRev: "123"}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.UpsertArtifactSubscription(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	sub.ConsumedVersion = 0
	sub.ConsumerRev = ""
	replay, err := s.UpsertArtifactSubscription(ctx, sub)
	if err != nil || replay != saved {
		t.Fatalf("subscribe again: %+v, wanted %+v: %v", replay, saved, err)
	}
}

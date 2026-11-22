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

package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kayyo321/taaro/internal/store"
)

func TestCopySQLiteLiveBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source, backup, restored := filepath.Join(root, "live.db"), filepath.Join(root, "backup #1.db"), filepath.Join(root, "restored.db")
	live, err := store.OpenSQLite(source)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	const repo = "acme/widgets"
	if err := live.UpsertPullRequest(ctx, store.PullRequestRecord{RepoFullName: repo, Number: 42, HeadSHA: "historical"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CopySQLite(ctx, source, backup); err != nil {
		t.Fatal(err)
	}
	if err := live.UpsertPullRequest(ctx, store.PullRequestRecord{RepoFullName: repo, Number: 42, HeadSHA: "current"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CopySQLite(ctx, source, backup); err == nil {
		t.Fatal("existing backup was accepted")
	}
	if err := os.WriteFile(restored+"-wal", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.CopySQLite(ctx, backup, restored); err == nil {
		t.Fatal("destination with a WAL sidecar was accepted")
	}
	if err := os.Remove(restored + "-wal"); err != nil {
		t.Fatal(err)
	}
	if err := store.CopySQLite(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	recovery, err := store.OpenSQLite(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if err := recovery.ValidateRepositoryScope(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := recovery.ValidateRepositoryScope(ctx, "acme/other"); err == nil || !strings.Contains(err.Error(), "outside the configured repository") {
		t.Fatalf("wrong repository scope was accepted: %v", err)
	}
	pr, err := recovery.GetPullRequest(ctx, repo, 42)
	if err != nil || pr.HeadSHA != "historical" {
		t.Fatalf("restored PR head = %q, err = %v", pr.HeadSHA, err)
	}
	if err := store.CopySQLite(ctx, backup, source); err == nil {
		t.Fatal("restore accepted the active database as its destination")
	}
	pr, err = live.GetPullRequest(ctx, repo, 42)
	if err != nil || pr.HeadSHA != "current" {
		t.Fatalf("active PR head after restore refusal = %q, err = %v", pr.HeadSHA, err)
	}
}

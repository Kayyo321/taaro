package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kayyo321/taaro/internal/server"
	"github.com/Kayyo321/taaro/internal/store"
)

func TestCLIArtifactRerunAndAwarenessFlow(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("test-root", nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "test-root"},
		{"-c", "user.name=Taaro Test", "-c", "user.email=taaro@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Test fixture"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	consumerRev := currentGitHead()
	if err := os.Mkdir("src", 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "src"))
	t.Setenv("TAARO_API_TOKEN", "test-token")
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	srv := &server.Server{AppID: 77, Store: st}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Publish a fixture with a shell command that verifies TAARO_FIXTURE_PATH content.
	if err := runArtifact([]string{
		"publish",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--key", "lexer/unterminated-string",
		"--rev", "abc1234",
		"--payload", "line1:opening-quote",
		"--command", `test -f test-root && test -z "${TAARO_API_TOKEN+x}" && grep -q "line1:opening-quote" "$TAARO_FIXTURE_PATH" && echo local-output`,
		"--pr", "247",
	}); err != nil {
		t.Fatalf("artifact publish: %v", err)
	}

	rerun := []string{
		"rerun",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--key", "lexer/unterminated-string",
		"--record-check",
		"--pr", "247",
	}
	if err := runArtifact(rerun); err == nil {
		t.Fatal("rerun executed without explicit consent")
	}
	if err := runArtifact(append(rerun, "--use-recorded-command")); err == nil {
		t.Fatal("rerun of an unpinned recorded command was accepted")
	}

	if err := runArtifact(append(rerun, "--use-recorded-command", "--version", "1")); err != nil {
		t.Fatalf("artifact rerun: %v", err)
	}
	if err := runArtifact(append(rerun, "--command", "printf modified > test-root")); err == nil || !strings.Contains(err.Error(), "record check: checkout has uncommitted changes") {
		t.Fatalf("expected mutated checkout to prevent recording, got %v", err)
	}
	if err := runArtifact(append(rerun, "--command", "exit 7")); err == nil || !strings.Contains(err.Error(), "--record-check requires a clean checkout") {
		t.Fatalf("expected dirty checkout to prevent recorded rerun, got %v", err)
	}
	if err := runArtifact(append(rerun, "--record-check=false", "--command", "test -s test-root")); err != nil {
		t.Fatalf("local-only rerun should accept dirty checkout: %v", err)
	}

	checks, err := st.ListValidationChecks(context.Background(), "acme/widgets", 247)
	if err != nil || len(checks) != 1 || checks[0].Conclusion != "success" || checks[0].ProducerRev != "abc1234" ||
		checks[0].ConsumerRev != consumerRev || checks[0].InputsHash != "" || checks[0].OutputSummary != "Local fixture rerun: success" {
		t.Fatalf("expected recorded rerun check, got %+v (%v)", checks, err)
	}

	// Upsert a proposal and verify awareness check exits 3 (hold) before receipt and 0 (allow) after receipt.
	if err := runProposal([]string{
		"upsert",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--key", "rfc/lexer",
		"--title", "Lexer contract",
		"--state", "accepted",
		"--paths", "internal/lexer/**",
	}); err != nil {
		t.Fatalf("proposal upsert: %v", err)
	}

	code, err := runAwareness([]string{
		"check",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--worker", "cli-w1",
		"--gen", "1",
		"--op", "write",
		"--paths", "internal/lexer/scan.go",
	})
	if err != nil || code != 3 {
		t.Fatalf("expected exit code 3 (hold) before receipt, got code=%d err=%v", code, err)
	}

	// Record delivery receipt and verify awareness check now exits 0 (allow).
	if code, err := runAwareness([]string{
		"receipt",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--worker", "cli-w1",
		"--gen", "1",
		"--items-json", `[{"kind":"proposal","key":"rfc/lexer","version":"v1","state":"accepted"}]`,
	}); err != nil || code != 0 {
		t.Fatalf("awareness receipt failed: code=%d err=%v", code, err)
	}

	code, err = runAwareness([]string{
		"check",
		"--server", ts.URL,
		"--repo", "acme/widgets",
		"--worker", "cli-w1",
		"--gen", "1",
		"--op", "write",
		"--paths", "internal/lexer/scan.go",
	})
	if err != nil || code != 0 {
		t.Fatalf("expected exit code 0 (allow) after receipt, got code=%d err=%v", code, err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/adapter"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "init", "new":
		if err := setupProject(os.Args[1], os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Printf("taaro %s (protocol v%s)\n", version.Version, version.ProtocolVersion)
	case "health":
		runHealth(os.Args[2:])
	case "context":
		runContext(os.Args[2:])
	case "artifact":
		exitIfErr(runArtifact(os.Args[2:]))
	case "proposal":
		exitIfErr(runProposal(os.Args[2:]))
	case "scope":
		exitIfErr(runScope(os.Args[2:]))
	case "awareness":
		code, err := runAwareness(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		if code != 0 {
			os.Exit(code)
		}
	case "overlap":
		exitIfErr(runOverlap(os.Args[2:]))
	case "check":
		exitIfErr(runCheck(os.Args[2:]))
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func exitIfErr(err error) {
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `taaro %s — coordination CLI for coding agents (protocol v%s)

Project setup:
  taaro init --repo owner/repo
  taaro new --repo owner/repo DIRECTORY
  taaro version

Observation & handoffs (Level 1):
  taaro health [--server URL]
  taaro context [--repo owner/repo] --pr NUMBER [--full] [--json] [--server URL]
  taaro proposal <upsert|list> [flags]
  taaro overlap <list|ack> [flags]

Fixture exchange & check identity (Level 2):
  taaro artifact <publish|list|get|subscribe|consumers|rerun> [flags]
  taaro check <record|match|list> [flags]

Enforced awareness & mediation (Level 3):
  taaro scope register [flags]
  taaro awareness <check|receipt|verify-pr> [flags]
`, version.Version, version.ProtocolVersion)
}

func defaultServerURL() string {
	if v := strings.TrimSpace(os.Getenv("TAARO_SERVER_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8080"
}

func defaultAPIToken() string {
	return strings.TrimSpace(os.Getenv("TAARO_API_TOKEN"))
}

func resolveRepo(explicit string) (string, error) {
	if explicit != "" {
		if !validRepo(explicit) {
			return "", fmt.Errorf("--repo requires a GitHub owner/repo (got %q)", explicit)
		}
		return explicit, nil
	}
	root, err := projectRoot(".")
	if err != nil {
		return "", fmt.Errorf("%v; pass --repo owner/repo or run taaro init", err)
	}
	p, err := readProject(root)
	if err != nil {
		return "", fmt.Errorf("%v; pass --repo owner/repo or run taaro init", err)
	}
	return p.Repo, nil
}

func runHealth(args []string) {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(*serverURL, "/") + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		os.Exit(1)
	}
	fmt.Println(strings.TrimSpace(string(body)))
}

func runContext(args []string) {
	fs := flag.NewFlagSet("context", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub repository in owner/repo format")
	pr := fs.Int("pr", 0, "pull request number")
	full := fs.Bool("full", false, "fetch full revision-anchored handoff card (reviews, checks, fixtures, proposals, overlaps)")
	asJSON := fs.Bool("json", false, "output raw JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	_ = fs.Parse(args)

	if *repo == "" && *pr > 0 {
		resolved, err := resolveRepo("")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		*repo = resolved
	}
	if !validRepo(*repo) || *pr <= 0 || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: --repo owner/repo and --pr NUMBER are required")
		os.Exit(2)
	}

	if *full {
		cli := adapter.NewClient(*serverURL, *token)
		card, err := cli.GetHandoff(context.Background(), *repo, *pr, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if *asJSON {
			printJSON(card)
			return
		}
		fmt.Println(card.SummaryMarkdown)
		return
	}

	endpoint := fmt.Sprintf(
		"%s/api/v1/context?repo=%s&pr=%s",
		strings.TrimRight(*serverURL, "/"),
		url.QueryEscape(*repo),
		url.QueryEscape(strconv.Itoa(*pr)),
	)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build request: %v\n", err)
		os.Exit(1)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read response: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	if *asJSON {
		fmt.Println(strings.TrimSpace(string(body)))
		return
	}

	var rec store.PullRequestRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	shortSHA := rec.HeadSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	fmt.Printf("%s #%d — %s\n", rec.RepoFullName, rec.Number, rec.Title)
	fmt.Printf("  state:       %s (draft=%t)\n", rec.State, rec.Draft)
	fmt.Printf("  head_sha:    %s (base=%s)\n", shortSHA, rec.BaseRef)
	fmt.Printf("  last_action: %s\n", rec.LastAction)
	fmt.Printf("  observed_at: %s\n", rec.ObservedAt.UTC().Format(time.RFC3339))
	if len(rec.LinkedIssues) > 0 {
		refs := make([]string, len(rec.LinkedIssues))
		for i, n := range rec.LinkedIssues {
			refs[i] = fmt.Sprintf("#%d", n)
		}
		fmt.Printf("  issues:      %s\n", strings.Join(refs, ", "))
	}
	if rec.CheckRunID > 0 && rec.CheckRunHead == rec.HeadSHA {
		fmt.Printf("  handoff_cr:  %d\n", rec.CheckRunID)
	}
	if rec.AwarenessCheckRunID > 0 && rec.AwarenessCheckHead == rec.HeadSHA {
		fmt.Printf("  awareness:   %s (check_run=%d)\n", rec.AwarenessConclusion, rec.AwarenessCheckRunID)
	}
	fmt.Printf("  url:         %s\n", rec.HTMLURL)
}

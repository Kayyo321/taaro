package main

import (
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

	"github.com/Kayyo321/taaro/internal/store"
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
	case "health":
		runHealth(os.Args[2:])
	case "context":
		runContext(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `taaro — coordination CLI for coding agents

Usage:
  taaro init --repo owner/repo
  taaro new --repo owner/repo DIRECTORY
  taaro health [--server URL]
  taaro context [--repo owner/repo] --pr NUMBER [--json] [--server URL]`)
}

func defaultServerURL() string {
	if v := strings.TrimSpace(os.Getenv("TAARO_SERVER_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8080"
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
	asJSON := fs.Bool("json", false, "output raw JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	_ = fs.Parse(args)

	if *repo == "" && *pr > 0 {
		root, err := projectRoot(".")
		if err == nil {
			var p project
			p, err = readProject(root)
			*repo = p.Repo
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v; pass --repo owner/repo or run taaro init\n", err)
			os.Exit(2)
		}
	}
	if !validRepo(*repo) || *pr <= 0 || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: --repo owner/repo and --pr NUMBER are required")
		os.Exit(2)
	}

	endpoint := fmt.Sprintf(
		"%s/api/v1/context?repo=%s&pr=%s",
		strings.TrimRight(*serverURL, "/"),
		url.QueryEscape(*repo),
		url.QueryEscape(strconv.Itoa(*pr)),
	)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
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
	if rec.CheckRunID > 0 && rec.CheckRunHead == rec.HeadSHA {
		fmt.Printf("  handoff_cr:  %d\n", rec.CheckRunID)
	}
	fmt.Printf("  url:         %s\n", rec.HTMLURL)
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kayyo321/taaro/internal/adapter"
	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/store"
)

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitIntCSV(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	var out []int
	for _, p := range parts {
		s := strings.TrimPrefix(strings.TrimSpace(p), "#")
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid issue number %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func currentGitHead() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runArtifact(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: taaro artifact <publish|list|get|subscribe|consumers|rerun> [flags]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "publish":
		return runArtifactPublish(rest)
	case "list":
		return runArtifactList(rest)
	case "get":
		return runArtifactGet(rest)
	case "subscribe":
		return runArtifactSubscribe(rest)
	case "consumers":
		return runArtifactConsumers(rest)
	case "rerun":
		return runArtifactRerun(rest)
	default:
		return fmt.Errorf("unknown artifact subcommand %q", sub)
	}
}

func runArtifactPublish(args []string) error {
	fs := flag.NewFlagSet("artifact publish", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "task-scoped artifact key (e.g. compiler/diagnostic-span)")
	rev := fs.String("rev", "", "producer revision SHA (defaults to current git HEAD)")
	file := fs.String("file", "", "path to file containing artifact payload")
	payload := fs.String("payload", "", "inline artifact payload")
	command := fs.String("command", "", "reproducer or compatibility test command")
	worker := fs.String("worker", "cli-producer", "producing worker identifier")
	prNum := fs.Int("pr", 0, "optional associated PR number")
	summary := fs.String("summary", "", "short human-readable summary")
	contentType := fs.String("content-type", "text/plain", "artifact MIME content type")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" || fs.NArg() != 0 {
		return errors.New("--key is required")
	}
	if *file != "" && *payload != "" {
		return errors.New("pass only one of --file and --payload")
	}
	producerRev := strings.TrimSpace(*rev)
	if producerRev == "" {
		producerRev = currentGitHead()
	}
	if producerRev == "" {
		return errors.New("--rev is required when not inside a Git checkout with commits")
	}

	body := *payload
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read --file: %w", err)
		}
		body = string(b)
	}
	if !utf8.ValidString(body) {
		return errors.New("artifact payload must be valid UTF-8")
	}

	cli := adapter.NewClient(*serverURL, *token)
	resp, err := cli.PublishArtifact(context.Background(), store.ArtifactRecord{
		RepoFullName:   repo,
		ArtifactKey:    strings.TrimSpace(*key),
		ProducerWorker: strings.TrimSpace(*worker),
		ProducerRev:    producerRev,
		PRNumber:       *prNum,
		Command:        strings.TrimSpace(*command),
		ContentType:    strings.TrimSpace(*contentType),
		Payload:        body,
		Summary:        strings.TrimSpace(*summary),
	})
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(resp)
		return nil
	}
	fmt.Printf("Published %s v%d (%s) from rev %s [%s]\n",
		resp.Artifact.ArtifactKey, resp.Artifact.Version, resp.Artifact.RepoFullName, resp.Artifact.ProducerRev, resp.Artifact.Checksum)
	if len(resp.StaleConsumers) > 0 {
		fmt.Printf("Stale consumers to notify (%d):\n", len(resp.StaleConsumers))
		for _, c := range resp.StaleConsumers {
			fmt.Printf("  - %s@gen%d (consumed v%d < latest v%d)\n", c.WorkerID, c.ContextGeneration, c.ConsumedVersion, resp.Artifact.Version)
		}
	}
	return nil
}

func runArtifactList(args []string) error {
	fs := flag.NewFlagSet("artifact list", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	cli := adapter.NewClient(*serverURL, *token)
	list, err := cli.ListArtifacts(context.Background(), repo)
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(list)
		return nil
	}
	if len(list) == 0 {
		fmt.Printf("No artifacts published for %s.\n", repo)
		return nil
	}
	for _, a := range list {
		fmt.Printf("%s\tv%d\trev=%s\tworker=%s\t%s\n", a.ArtifactKey, a.Version, a.ProducerRev, a.ProducerWorker, a.Summary)
	}
	return nil
}

func runArtifactGet(args []string) error {
	fs := flag.NewFlagSet("artifact get", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "artifact key")
	ver := fs.Int("version", 0, "exact immutable version (0 for latest)")
	outPath := fs.String("out", "", "optional file path to write artifact payload")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" || *ver < 0 || fs.NArg() != 0 {
		return errors.New("artifact get requires --key and accepts only flags with a nonnegative --version")
	}
	cli := adapter.NewClient(*serverURL, *token)
	art, err := cli.GetArtifact(context.Background(), repo, strings.TrimSpace(*key), *ver)
	if err != nil {
		return err
	}
	if err := verifyArtifact(art, repo, strings.TrimSpace(*key), *ver); err != nil {
		return err
	}
	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(art.Payload), 0o644); err != nil {
			return fmt.Errorf("write --out %s: %w", *outPath, err)
		}
	}
	if *asJSON {
		printJSON(art)
		return nil
	}
	fmt.Printf("%s v%d (rev=%s, worker=%s, checksum=%s)\n", art.ArtifactKey, art.Version, art.ProducerRev, art.ProducerWorker, art.Checksum)
	if art.Command != "" {
		fmt.Printf("  command: %q\n", art.Command)
	}
	if *outPath != "" {
		fmt.Printf("  written: %s\n", *outPath)
	} else if art.Payload != "" {
		fmt.Printf("---\n%s\n", art.Payload)
	}
	return nil
}

func runArtifactSubscribe(args []string) error {
	fs := flag.NewFlagSet("artifact subscribe", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "artifact key")
	worker := fs.String("worker", "", "consumer worker ID")
	gen := fs.Int("gen", 1, "worker context generation")
	consumedVer := fs.Int("consumed-version", 0, "artifact version consumed by worker")
	consumerRev := fs.String("rev", "", "consumer checkout SHA (defaults to current git HEAD)")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" || strings.TrimSpace(*worker) == "" {
		return errors.New("--key and --worker are required")
	}
	cRev := strings.TrimSpace(*consumerRev)
	if cRev == "" {
		cRev = currentGitHead()
	}
	cli := adapter.NewClient(*serverURL, *token)
	sub, err := cli.SubscribeArtifact(context.Background(), store.ArtifactSubscription{
		RepoFullName:      repo,
		ArtifactKey:       strings.TrimSpace(*key),
		WorkerID:          strings.TrimSpace(*worker),
		ContextGeneration: *gen,
		ConsumedVersion:   *consumedVer,
		ConsumerRev:       cRev,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(sub)
		return nil
	}
	fmt.Printf("Subscribed %s@gen%d to %s (consumed=v%d, latest=v%d, stale=%t)\n",
		sub.WorkerID, sub.ContextGeneration, sub.ArtifactKey, sub.ConsumedVersion, sub.LatestVersion, sub.Stale)
	return nil
}

func runArtifactConsumers(args []string) error {
	fs := flag.NewFlagSet("artifact consumers", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "artifact key")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return errors.New("--key is required")
	}
	cli := adapter.NewClient(*serverURL, *token)
	list, err := cli.ListArtifactConsumers(context.Background(), repo, strings.TrimSpace(*key))
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(list)
		return nil
	}
	if len(list) == 0 {
		fmt.Printf("No consumers registered for %s.\n", *key)
		return nil
	}
	for _, c := range list {
		fmt.Printf("%s@gen%d\tconsumed=v%d\tlatest=v%d\tstale=%t\trev=%s\n",
			c.WorkerID, c.ContextGeneration, c.ConsumedVersion, c.LatestVersion, c.Stale, c.ConsumerRev)
	}
	return nil
}

func verifyArtifact(art store.ArtifactRecord, repo, key string, version int) error {
	if art.RepoFullName != repo || art.ArtifactKey != key || art.Version < 1 || (version != 0 && art.Version != version) {
		return errors.New("server returned a different artifact than requested")
	}
	checksum := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(art.Payload)))
	if art.Checksum != checksum {
		return errors.New("artifact payload checksum does not match")
	}
	return nil
}

func requireCleanCheckout(root, revision string) error {
	head, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("resolve checkout HEAD: %w", err)
	}
	if strings.TrimSpace(string(head)) != revision {
		return errors.New("checkout HEAD changed")
	}
	status, err := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--untracked-files=normal", "--ignore-submodules=none").Output()
	if err != nil {
		return fmt.Errorf("inspect checkout: %w", err)
	}
	if len(status) != 0 {
		return errors.New("checkout has uncommitted changes")
	}
	return nil
}

func runArtifactRerun(args []string) error {
	fs := flag.NewFlagSet("artifact rerun", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "artifact key")
	ver := fs.Int("version", 0, "exact immutable version (0 for latest)")
	cmdOverride := fs.String("command", "", "local shell command to run with the fixture")
	useRecorded := fs.Bool("use-recorded-command", false, "execute the reviewed recorded shell command for an exact --version")
	recordCheck := fs.Bool("record-check", false, "record the rerun outcome as a combined validation check")
	jobName := fs.String("job", "", "validation job name (defaults to artifact key)")
	prNum := fs.Int("pr", 0, "optional PR number for recorded validation check")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	artifactKey := strings.TrimSpace(*key)
	if artifactKey == "" || *ver < 0 || *prNum < 0 || fs.NArg() != 0 {
		return errors.New("artifact rerun requires --key and accepts only flags with nonnegative --version and --pr")
	}
	runCmd := strings.TrimSpace(*cmdOverride)
	switch {
	case *useRecorded && (runCmd != "" || *ver == 0):
		return errors.New("--use-recorded-command requires an exact --version and cannot be combined with --command")
	case !*useRecorded && runCmd == "":
		return errors.New("pass --command or review the artifact with artifact get and pass --use-recorded-command with its exact --version")
	}
	root, err := projectRoot(".")
	if err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	consumerRev := currentGitHead()
	if consumerRev == "" {
		return errors.New("artifact rerun requires a Git checkout with a committed HEAD")
	}

	ctx := context.Background()
	cli := adapter.NewClient(*serverURL, *token)
	art, err := cli.GetArtifact(ctx, repo, artifactKey, *ver)
	if err != nil {
		return err
	}
	if err := verifyArtifact(art, repo, artifactKey, *ver); err != nil {
		return err
	}
	if *useRecorded {
		runCmd = strings.TrimSpace(art.Command)
	}
	if runCmd == "" {
		return errors.New("artifact has no recorded command; pass --command")
	}

	tmp, err := os.CreateTemp("", "taaro-fixture-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(art.Payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if *recordCheck {
		if err := requireCleanCheckout(root, consumerRev); err != nil {
			return fmt.Errorf("--record-check requires a clean checkout: %w", err)
		}
	}
	started := time.Now().UTC()
	cmd := exec.Command("sh", "-c", runCmd)
	cmd.Dir = root
	for _, value := range cmd.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !strings.EqualFold(name, "TAARO_API_TOKEN") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env,
		"TAARO_FIXTURE_PATH="+tmp.Name(),
		"TAARO_ARTIFACT_KEY="+art.ArtifactKey,
		"TAARO_ARTIFACT_VERSION="+strconv.Itoa(art.Version),
		"TAARO_PRODUCER_REV="+art.ProducerRev,
		"TAARO_CONSUMER_REV="+consumerRev,
	)
	out, runErr := cmd.CombinedOutput()
	completed := time.Now().UTC()

	conclusion := "success"
	if runErr != nil {
		conclusion = "failure"
	}

	var (
		recorded  *store.ValidationCheckRecord
		recordErr error
	)
	if *recordCheck {
		recordErr = requireCleanCheckout(root, consumerRev)
	}
	if *recordCheck && recordErr == nil {
		jName := strings.TrimSpace(*jobName)
		if jName == "" {
			jName = "fixture:" + art.ArtifactKey
		}
		res, err := cli.RecordCheck(ctx, store.ValidationCheckRecord{
			RepoFullName:    repo,
			JobName:         jName,
			PRNumber:        *prNum,
			ProducerRev:     art.ProducerRev,
			ConsumerRev:     consumerRev,
			ArtifactKey:     art.ArtifactKey,
			ArtifactVersion: art.Version,
			Status:          "completed",
			Conclusion:      conclusion,
			OutputSummary:   "Local fixture rerun: " + conclusion,
			StartedAt:       started,
			CompletedAt:     completed,
		})
		if err == nil {
			recorded = &res.Check
		}
		recordErr = err
	}

	if *asJSON {
		printJSON(map[string]any{
			"artifact_key":     art.ArtifactKey,
			"artifact_version": art.Version,
			"producer_rev":     art.ProducerRev,
			"consumer_rev":     consumerRev,
			"command":          runCmd,
			"conclusion":       conclusion,
			"output":           string(out),
			"recorded_check":   recorded,
		})
	} else {
		fmt.Printf("Rerun %s v%d: %s (producer=%s, consumer=%s)\n",
			art.ArtifactKey, art.Version, conclusion, art.ProducerRev, consumerRev)
		if len(out) > 0 {
			fmt.Print(string(out))
		}
	}
	if recordErr != nil {
		recordErr = fmt.Errorf("record check: %w", recordErr)
	}
	if runErr != nil {
		runErr = fmt.Errorf("fixture rerun failed: %w", runErr)
	}
	return errors.Join(runErr, recordErr)
}

func runProposal(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: taaro proposal <upsert|list> [flags]")
	}
	switch args[0] {
	case "upsert":
		return runProposalUpsert(args[1:])
	case "list":
		return runProposalList(args[1:])
	default:
		return fmt.Errorf("unknown proposal subcommand %q", args[0])
	}
}

func runProposalUpsert(args []string) error {
	fs := flag.NewFlagSet("proposal upsert", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	key := fs.String("key", "", "proposal key (e.g. rfc/diagnostic-span)")
	title := fs.String("title", "", "proposal title")
	state := fs.String("state", "draft", "lifecycle state: draft, accepted, withdrawn, superseded")
	supersededBy := fs.String("superseded-by", "", "replacement proposal key if superseded")
	sourceType := fs.String("source-type", "rfc", "source type: rfc, issue, pr")
	sourceRef := fs.String("source-ref", "", "source reference (path or #number)")
	sourceURL := fs.String("source-url", "", "authoritative source URL")
	owner := fs.String("owner", "", "proposal owner")
	paths := fs.String("paths", "", "comma-separated scope path patterns")
	interfaces := fs.String("interfaces", "", "comma-separated named interfaces/components")
	issues := fs.String("issues", "", "comma-separated linked issue numbers")
	summary := fs.String("summary", "", "concise proposal summary")
	excerpt := fs.String("excerpt", "", "quoted project data excerpt")
	visibility := fs.String("visibility", "public", "visibility: public or private")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" || strings.TrimSpace(*title) == "" {
		return errors.New("--key and --title are required")
	}
	issueNums, err := splitIntCSV(*issues)
	if err != nil {
		return err
	}
	cli := adapter.NewClient(*serverURL, *token)
	rec, err := cli.UpsertProposal(context.Background(), store.ProposalRecord{
		RepoFullName: repo,
		ProposalKey:  strings.TrimSpace(*key),
		SourceType:   strings.TrimSpace(*sourceType),
		SourceRef:    strings.TrimSpace(*sourceRef),
		SourceURL:    strings.TrimSpace(*sourceURL),
		Title:        strings.TrimSpace(*title),
		Owner:        strings.TrimSpace(*owner),
		State:        strings.TrimSpace(*state),
		SupersededBy: strings.TrimSpace(*supersededBy),
		ScopePaths:   splitCSV(*paths),
		Interfaces:   splitCSV(*interfaces),
		LinkedIssues: issueNums,
		Summary:      strings.TrimSpace(*summary),
		Excerpt:      strings.TrimSpace(*excerpt),
		Visibility:   strings.TrimSpace(*visibility),
	})
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(rec)
		return nil
	}
	fmt.Printf("Proposal %s v%d (%s, visibility=%s): %s\n", rec.ProposalKey, rec.Version, rec.State, rec.Visibility, rec.Title)
	return nil
}

func runProposalList(args []string) error {
	fs := flag.NewFlagSet("proposal list", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	audience := fs.String("audience", "", "audience visibility: authorized or public")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	cli := adapter.NewClient(*serverURL, *token)
	list, err := cli.ListProposals(context.Background(), repo, *audience)
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(list)
		return nil
	}
	if len(list) == 0 {
		fmt.Printf("No proposals indexed for %s.\n", repo)
		return nil
	}
	for _, p := range list {
		fmt.Printf("%s\tv%d\t%s\t(%s)\t%s\n", p.ProposalKey, p.Version, p.State, p.Visibility, p.Title)
	}
	return nil
}

func runScope(args []string) error {
	if len(args) < 1 || args[0] != "register" {
		return errors.New("usage: taaro scope register [flags]")
	}
	fs := flag.NewFlagSet("scope register", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	worker := fs.String("worker", "", "acting worker ID")
	gen := fs.Int("gen", 1, "worker context generation")
	parent := fs.String("parent", "", "optional parent worker ID")
	role := fs.String("role", "", "worker role")
	prNum := fs.Int("pr", 0, "optional PR number")
	baseRev := fs.String("base-rev", "", "base commit SHA")
	paths := fs.String("paths", "", "comma-separated active paths")
	interfaces := fs.String("interfaces", "", "comma-separated named interfaces")
	issues := fs.String("issues", "", "comma-separated linked issue numbers")
	ttlSec := fs.Int("ttl", 1800, "scope TTL in seconds")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*worker) == "" {
		return errors.New("--worker is required")
	}
	issueNums, err := splitIntCSV(*issues)
	if err != nil {
		return err
	}
	bRev := strings.TrimSpace(*baseRev)
	if bRev == "" {
		bRev = currentGitHead()
	}
	cli := adapter.NewClient(*serverURL, *token)
	resp, err := cli.RegisterScope(context.Background(), coordination.RegisterScopeRequest{
		RepoFullName:      repo,
		WorkerID:          strings.TrimSpace(*worker),
		ContextGeneration: *gen,
		ParentWorkerID:    strings.TrimSpace(*parent),
		Role:              strings.TrimSpace(*role),
		PRNumber:          *prNum,
		BaseRev:           bRev,
		Paths:             splitCSV(*paths),
		Interfaces:        splitCSV(*interfaces),
		IssueLinks:        issueNums,
		TTL:               time.Duration(*ttlSec) * time.Second,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(resp)
		return nil
	}
	fmt.Printf("Registered scope for %s@gen%d (matches=%d, silent=%t, hash=%s)\n",
		resp.Scope.WorkerID, resp.Scope.ContextGeneration, len(resp.Evidence), resp.Silent, resp.EvidenceSetHash)
	for _, ev := range resp.Evidence {
		fmt.Printf("  - [%s] %s %s (%s): %s\n", ev.Kind, ev.Key, ev.Version, ev.MatchReason, ev.Title)
	}
	return nil
}

func runAwareness(args []string) (int, error) {
	if len(args) < 1 {
		return 2, errors.New("usage: taaro awareness <check|receipt|verify-pr> [flags]")
	}
	switch args[0] {
	case "check":
		code, err := runAwarenessCheck(args[1:])
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return code, err
	case "receipt":
		if err := runAwarenessReceipt(args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
			return 1, err
		}
		return 0, nil
	case "verify-pr":
		if err := runAwarenessVerifyPR(args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
			return 1, err
		}
		return 0, nil
	default:
		return 2, fmt.Errorf("unknown awareness subcommand %q", args[0])
	}
}

func runAwarenessCheck(args []string) (int, error) {
	fs := flag.NewFlagSet("awareness check", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	worker := fs.String("worker", "", "acting worker ID")
	gen := fs.Int("gen", 1, "worker context generation")
	op := fs.String("op", "write", "protected operation: read, write, plan, publish")
	prNum := fs.Int("pr", 0, "optional PR number")
	head := fs.String("head-sha", "", "optional PR head SHA for changed-path lookup")
	paths := fs.String("paths", "", "comma-separated target paths")
	interfaces := fs.String("interfaces", "", "comma-separated named interfaces")
	issues := fs.String("issues", "", "comma-separated linked issue numbers")
	audience := fs.String("audience", "", "audience: authorized or public")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() != 0 || *gen < 1 || *prNum < 0 {
		return 2, errors.New("awareness check accepts only flags, a positive --gen, and a nonnegative --pr")
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return 2, err
	}
	if strings.TrimSpace(*worker) == "" {
		return 2, errors.New("--worker is required")
	}
	issueNums, err := splitIntCSV(*issues)
	if err != nil {
		return 2, err
	}

	cli := adapter.NewClient(*serverURL, *token)
	resp, err := cli.EvaluateAction(context.Background(), coordination.EvaluateActionRequest{
		RepoFullName:      repo,
		WorkerID:          strings.TrimSpace(*worker),
		ContextGeneration: *gen,
		Operation:         strings.TrimSpace(*op),
		PRNumber:          *prNum,
		HeadSHA:           strings.TrimSpace(*head),
		TargetPaths:       splitCSV(*paths),
		Interfaces:        splitCSV(*interfaces),
		LinkedIssues:      issueNums,
		Audience:          strings.TrimSpace(*audience),
	})
	if err != nil {
		return 3, err
	}
	if *asJSON {
		printJSON(resp)
	} else if !resp.Silent {
		fmt.Printf("Decision: %s (receipt_verified=%t, hash=%s)\n",
			resp.Decision, resp.ReceiptVerified, resp.EvidenceSetHash)
		if resp.Reason != "" {
			fmt.Printf("  reason: %s\n", resp.Reason)
		}
		for _, ev := range resp.MissingOrStaleEvidence {
			fmt.Printf("  quoted-data [%s] %q %q (%q): %q\n", ev.Kind, ev.Key, ev.Version, ev.LifecycleState, ev.Title)
			fmt.Printf("    authority=%q match=%q source=%q\n", ev.Authority, ev.MatchReason, ev.SourceLink)
			if ev.Summary != "" {
				fmt.Printf("    summary: %q\n", ev.Summary)
			}
			if ev.QuotedExcerpt != "" {
				fmt.Printf("    excerpt: %q\n", ev.QuotedExcerpt)
			}
		}
	}
	if resp.Decision == "allow" || resp.Decision == "allow_with_context" {
		return 0, nil
	}
	return 3, nil
}

func runAwarenessReceipt(args []string) error {
	fs := flag.NewFlagSet("awareness receipt", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	worker := fs.String("worker", "", "acting worker ID")
	gen := fs.Int("gen", 1, "worker context generation")
	itemsJSON := fs.String("items-json", "[]", "JSON array of receipt items [{kind,key,version,state}]")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*worker) == "" {
		return errors.New("--worker is required")
	}
	var items []store.ReceiptItem
	if err := json.Unmarshal([]byte(*itemsJSON), &items); err != nil {
		return fmt.Errorf("parse --items-json: %w", err)
	}
	cli := adapter.NewClient(*serverURL, *token)
	rec, err := cli.RecordReceipt(context.Background(), repo, strings.TrimSpace(*worker), *gen, items)
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(rec)
		return nil
	}
	fmt.Printf("Recorded receipt #%d for %s@gen%d (hash=%s, items=%d)\n",
		rec.ID, rec.WorkerID, rec.ContextGeneration, rec.EvidenceSetHash, len(rec.Items))
	return nil
}

func runAwarenessVerifyPR(args []string) error {
	fs := flag.NewFlagSet("awareness verify-pr", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
	prNum := fs.Int("pr", 0, "PR number")
	headSHA := fs.String("head-sha", "", "candidate head SHA (or merge-group SHA)")
	revKind := fs.String("revision-kind", "pr_head", "revision kind: pr_head or merge_group")
	worker := fs.String("worker", "", "optional reviewing/implementing worker ID")
	gen := fs.Int("gen", 0, "optional worker context generation")
	paths := fs.String("paths", "", "comma-separated changed paths")
	interfaces := fs.String("interfaces", "", "comma-separated touched interfaces")
	issues := fs.String("issues", "", "comma-separated linked issue numbers")
	publish := fs.Bool("publish", false, "publish required Taaro Awareness Check to GitHub")
	asJSON := fs.Bool("json", false, "output JSON")
	serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
	token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		return err
	}
	sha := strings.TrimSpace(*headSHA)
	if sha == "" {
		sha = currentGitHead()
	}
	if sha == "" {
		return errors.New("--head-sha is required")
	}
	issueNums, err := splitIntCSV(*issues)
	if err != nil {
		return err
	}

	cli := adapter.NewClient(*serverURL, *token)
	resp, err := cli.VerifyCandidate(context.Background(), coordination.CandidateAwarenessRequest{
		RepoFullName:      repo,
		PRNumber:          *prNum,
		HeadSHA:           sha,
		RevisionKind:      strings.TrimSpace(*revKind),
		WorkerID:          strings.TrimSpace(*worker),
		ContextGeneration: *gen,
		TargetPaths:       splitCSV(*paths),
		Interfaces:        splitCSV(*interfaces),
		LinkedIssues:      issueNums,
	}, *publish)
	if err != nil {
		return err
	}
	if *asJSON {
		printJSON(resp)
		return nil
	}
	fmt.Printf("Awareness check on %s (%s): %s (complete=%t)\n  %s\n",
		resp.Evaluation.HeadSHA, resp.Evaluation.RevisionKind, resp.Evaluation.Verdict, resp.Evaluation.Complete, resp.Evaluation.Reason)
	return nil
}

func runOverlap(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: taaro overlap <list|ack> [flags]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("overlap list", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
		prNum := fs.Int("pr", 0, "optional PR number filter")
		all := fs.Bool("all", false, "include acknowledged overlaps")
		asJSON := fs.Bool("json", false, "output JSON")
		serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
		token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, err := resolveRepo(*repoFlag)
		if err != nil {
			return err
		}
		cli := adapter.NewClient(*serverURL, *token)
		list, err := cli.ListOverlaps(context.Background(), repo, *prNum, *all)
		if err != nil {
			return err
		}
		if *asJSON {
			printJSON(list)
			return nil
		}
		if len(list) == 0 {
			fmt.Printf("No active overlaps detected for %s.\n", repo)
			return nil
		}
		for _, o := range list {
			fmt.Printf("PR #%d <-> PR #%d\t[%s]\t(source=%s, ack=%t %s)\n",
				o.PRA, o.PRB, strings.Join(o.MatchReasons, ", "), o.Source, o.Acknowledged, o.Disposition)
		}
		return nil

	case "ack":
		fs := flag.NewFlagSet("overlap ack", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
		prA := fs.Int("pr-a", 0, "first PR number")
		prB := fs.Int("pr-b", 0, "second PR number")
		disposition := fs.String("disposition", "", "complementary, combined, or dismissed")
		actor := fs.String("actor", "", "acknowledging author or handle")
		note := fs.String("note", "", "optional rationale note")
		asJSON := fs.Bool("json", false, "output JSON")
		serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
		token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, err := resolveRepo(*repoFlag)
		if err != nil {
			return err
		}
		cli := adapter.NewClient(*serverURL, *token)
		ack, err := cli.AcknowledgeOverlap(context.Background(), repo, *prA, *prB, *disposition, *actor, *note)
		if err != nil {
			return err
		}
		if *asJSON {
			printJSON(ack)
			return nil
		}
		fmt.Printf("Acknowledged overlap PR #%d <-> PR #%d as %s\n", ack.PRA, ack.PRB, ack.Disposition)
		return nil

	default:
		return fmt.Errorf("unknown overlap subcommand %q", args[0])
	}
}

func runCheck(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: taaro check <record|match|list> [flags]")
	}
	switch args[0] {
	case "record":
		fs := flag.NewFlagSet("check record", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
		job := fs.String("job", "", "job name")
		prNum := fs.Int("pr", 0, "optional PR number")
		prodRev := fs.String("producer-rev", "", "producer revision SHA")
		consRev := fs.String("consumer-rev", "", "consumer revision SHA")
		inputsHash := fs.String("inputs-hash", "", "deterministic inputs digest")
		toolchain := fs.String("toolchain", runtime.Version(), "toolchain identifier")
		flagsStr := fs.String("flags", "", "build/test flags")
		env := fs.String("env", runtime.GOOS+"-"+runtime.GOARCH, "execution environment")
		artKey := fs.String("artifact-key", "", "optional artifact key")
		artVer := fs.Int("artifact-version", 0, "optional artifact version")
		status := fs.String("status", "completed", "queued, in_progress, or completed")
		conclusion := fs.String("conclusion", "", "completed check conclusion: success (default), failure, or cancelled")
		flaky := fs.Bool("flaky", false, "mark check as flaky (ineligible for reuse)")
		summary := fs.String("summary", "", "output summary")
		asJSON := fs.Bool("json", false, "output JSON")
		serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
		token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, err := resolveRepo(*repoFlag)
		if err != nil {
			return err
		}
		checkStatus := strings.TrimSpace(*status)
		checkConclusion := strings.TrimSpace(*conclusion)
		if checkStatus == "completed" && checkConclusion == "" {
			checkConclusion = "success"
		}
		cli := adapter.NewClient(*serverURL, *token)
		resp, err := cli.RecordCheck(context.Background(), store.ValidationCheckRecord{
			RepoFullName:    repo,
			JobName:         strings.TrimSpace(*job),
			PRNumber:        *prNum,
			ProducerRev:     strings.TrimSpace(*prodRev),
			ConsumerRev:     strings.TrimSpace(*consRev),
			InputsHash:      strings.TrimSpace(*inputsHash),
			Toolchain:       strings.TrimSpace(*toolchain),
			Flags:           strings.TrimSpace(*flagsStr),
			Environment:     strings.TrimSpace(*env),
			ArtifactKey:     strings.TrimSpace(*artKey),
			ArtifactVersion: *artVer,
			Status:          checkStatus,
			Conclusion:      checkConclusion,
			Flaky:           *flaky,
			OutputSummary:   strings.TrimSpace(*summary),
		})
		if err != nil {
			return err
		}
		if *asJSON {
			printJSON(resp)
			return nil
		}
		fmt.Printf("Recorded check #%d (%s): %s/%s (producer=%s, consumer=%s, cancelled=%d)\n",
			resp.Check.ID, resp.Check.JobName, resp.Check.Status, resp.Check.Conclusion,
			resp.Check.ProducerRev, resp.Check.ConsumerRev, len(resp.CancelledIDs))
		return nil

	case "match":
		fs := flag.NewFlagSet("check match", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
		job := fs.String("job", "", "job name")
		prodRev := fs.String("producer-rev", "", "producer revision SHA")
		consRev := fs.String("consumer-rev", "", "consumer revision SHA")
		inputsHash := fs.String("inputs-hash", "", "deterministic inputs digest")
		toolchain := fs.String("toolchain", runtime.Version(), "toolchain identifier")
		flagsStr := fs.String("flags", "", "build/test flags")
		env := fs.String("env", runtime.GOOS+"-"+runtime.GOARCH, "execution environment")
		asJSON := fs.Bool("json", false, "output JSON")
		serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
		token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, err := resolveRepo(*repoFlag)
		if err != nil {
			return err
		}
		cli := adapter.NewClient(*serverURL, *token)
		resp, err := cli.MatchCheck(context.Background(), coordination.MatchCheckRequest{
			RepoFullName: repo,
			JobName:      strings.TrimSpace(*job),
			ProducerRev:  strings.TrimSpace(*prodRev),
			ConsumerRev:  strings.TrimSpace(*consRev),
			InputsHash:   strings.TrimSpace(*inputsHash),
			Toolchain:    strings.TrimSpace(*toolchain),
			Flags:        strings.TrimSpace(*flagsStr),
			Environment:  strings.TrimSpace(*env),
		})
		if err != nil {
			return err
		}
		if *asJSON {
			printJSON(resp)
			return nil
		}
		fmt.Printf("Reusable: %t — %s\n", resp.Reusable, resp.Reason)
		if resp.Check != nil {
			fmt.Printf("  check #%d: status=%s conclusion=%s\n", resp.Check.ID, resp.Check.Status, resp.Check.Conclusion)
		}
		return nil

	case "list":
		fs := flag.NewFlagSet("check list", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "GitHub repository (owner/repo)")
		prNum := fs.Int("pr", 0, "optional PR number")
		asJSON := fs.Bool("json", false, "output JSON")
		serverURL := fs.String("server", defaultServerURL(), "taaro-server base URL")
		token := fs.String("token", defaultAPIToken(), "optional API Bearer token")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, err := resolveRepo(*repoFlag)
		if err != nil {
			return err
		}
		cli := adapter.NewClient(*serverURL, *token)
		checks, err := cli.ListChecks(context.Background(), repo, *prNum)
		if err != nil {
			return err
		}
		if *asJSON {
			printJSON(checks)
			return nil
		}
		if len(checks) == 0 {
			fmt.Printf("No validation checks recorded for %s.\n", repo)
			return nil
		}
		for _, c := range checks {
			fmt.Printf("#%d\t%s\t%s/%s\tproducer=%s\tconsumer=%s\tflaky=%t\n",
				c.ID, c.JobName, c.Status, c.Conclusion, c.ProducerRev, c.ConsumerRev, c.Flaky)
		}
		return nil

	default:
		return fmt.Errorf("unknown check subcommand %q", args[0])
	}
}

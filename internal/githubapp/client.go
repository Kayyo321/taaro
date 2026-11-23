package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
)

const (
	HandoffCheckName   = "Taaro Handoff"
	AwarenessCheckName = "Taaro Awareness Check"
)

// HandoffCheckInput describes the revision-anchored handoff card to publish on GitHub.
type HandoffCheckInput struct {
	InstallationID   int64
	Owner            string
	Repo             string
	PRNumber         int
	HeadSHA          string
	Title            string
	SummaryMarkdown  string
	DetailsURL       string
	ExistingCheckRun int64
	ObservedAt       time.Time
}

// AwarenessCheckInput describes the required Taaro Awareness Check run pinned to a candidate SHA
// (PR head or merge-group SHA). Per docs/PROJECT.md §3, "neutral" and "skipped" conclusions are
// strictly prohibited for required awareness evaluations.
type AwarenessCheckInput struct {
	InstallationID   int64
	Owner            string
	Repo             string
	PRNumber         int
	HeadSHA          string
	RevisionKind     string // "pr_head" or "merge_group"
	Status           string // "in_progress" or "completed"
	Conclusion       string // "success", "action_required", or "failure" (when completed)
	Title            string
	SummaryMarkdown  string
	DetailsURL       string
	ExistingCheckRun int64
	ObservedAt       time.Time
}

// HandoffPublisher publishes or updates a revision-anchored neutral Check Run.
type HandoffPublisher interface {
	PublishHandoffCheck(ctx context.Context, in HandoffCheckInput) (int64, error)
}

// AwarenessPublisher publishes or updates the dedicated required "Taaro Awareness Check" Check Run.
type AwarenessPublisher interface {
	PublishAwarenessCheck(ctx context.Context, in AwarenessCheckInput) (int64, error)
}

// Client manages per-installation GitHub API clients authenticated via App JWT -> Installation Token.
type Client struct {
	appID          int64
	privateKeyPath string
	baseTransport  http.RoundTripper

	mu           sync.Mutex
	installCache map[int64]*github.Client
}

// NewClient constructs a GitHub App client factory using a PEM private key file.
func NewClient(appID int64, privateKeyPath string) *Client {
	return &Client{
		appID:          appID,
		privateKeyPath: privateKeyPath,
		baseTransport:  http.DefaultTransport,
		installCache:   make(map[int64]*github.Client),
	}
}

func (c *Client) forInstallation(installationID int64) (*github.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cli, ok := c.installCache[installationID]; ok {
		return cli, nil
	}

	itr, err := ghinstallation.NewKeyFromFile(c.baseTransport, c.appID, installationID, c.privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("create installation transport (app=%d installation=%d): %w", c.appID, installationID, err)
	}

	cli, err := github.NewClient(github.WithHTTPClient(&http.Client{
		Transport: itr,
		Timeout:   15 * time.Second,
	}))
	if err != nil {
		return nil, fmt.Errorf("create github client (app=%d installation=%d): %w", c.appID, installationID, err)
	}
	c.installCache[installationID] = cli
	return cli, nil
}

// PublishHandoffCheck creates or updates the completed neutral "Taaro Handoff" Check Run
// pinned to the exact PR head SHA.
func (c *Client) PublishHandoffCheck(ctx context.Context, in HandoffCheckInput) (int64, error) {
	cli, err := c.forInstallation(in.InstallationID)
	if err != nil {
		return 0, err
	}

	now := in.ObservedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	externalID := fmt.Sprintf("taaro-handoff:%s/%s#%d@%s", in.Owner, in.Repo, in.PRNumber, in.HeadSHA)
	output := &github.CheckRunOutput{
		Title:   github.Ptr(in.Title),
		Summary: github.Ptr(in.SummaryMarkdown),
	}

	var detailsURL *string
	if strings.HasPrefix(in.DetailsURL, "https://") {
		detailsURL = github.Ptr(in.DetailsURL)
	}

	if in.ExistingCheckRun > 0 {
		updated, resp, err := cli.Checks.UpdateCheckRun(ctx, in.Owner, in.Repo, in.ExistingCheckRun, github.UpdateCheckRunOptions{
			Name:        HandoffCheckName,
			DetailsURL:  detailsURL,
			ExternalID:  github.Ptr(externalID),
			Status:      github.Ptr("completed"),
			Conclusion:  github.Ptr("neutral"),
			CompletedAt: &github.Timestamp{Time: now},
			Output:      output,
		})
		if err == nil {
			return updated.GetID(), nil
		}
		if resp == nil || resp.StatusCode != http.StatusNotFound {
			return 0, fmt.Errorf("update handoff check run: %w", err)
		}
	}

	created, _, err := cli.Checks.CreateCheckRun(ctx, in.Owner, in.Repo, github.CreateCheckRunOptions{
		Name:        HandoffCheckName,
		HeadSHA:     in.HeadSHA,
		DetailsURL:  detailsURL,
		ExternalID:  github.Ptr(externalID),
		Status:      github.Ptr("completed"),
		Conclusion:  github.Ptr("neutral"),
		StartedAt:   &github.Timestamp{Time: now},
		CompletedAt: &github.Timestamp{Time: now},
		Output:      output,
	})
	if err != nil {
		return 0, fmt.Errorf("create handoff check run for %s/%s@%s: %w", in.Owner, in.Repo, in.HeadSHA, err)
	}
	return created.GetID(), nil
}

// PublishAwarenessCheck creates or updates the dedicated required "Taaro Awareness Check" Check Run.
// It rejects "neutral" and "skipped" conclusions to uphold GitHub required-check semantics.
func (c *Client) PublishAwarenessCheck(ctx context.Context, in AwarenessCheckInput) (int64, error) {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = "completed"
	}
	conclusion := strings.ToLower(strings.TrimSpace(in.Conclusion))

	switch status {
	case "in_progress":
		if conclusion != "" {
			return 0, fmt.Errorf("in_progress awareness check must not specify a conclusion (got %q)", in.Conclusion)
		}
	case "completed":
		switch conclusion {
		case "success", "action_required", "failure":
		case "neutral", "skipped":
			return 0, fmt.Errorf("conclusion %q is invalid for required %s", conclusion, AwarenessCheckName)
		default:
			return 0, fmt.Errorf("unsupported awareness check conclusion %q", in.Conclusion)
		}
	default:
		return 0, fmt.Errorf("unsupported awareness check status %q", in.Status)
	}

	cli, err := c.forInstallation(in.InstallationID)
	if err != nil {
		return 0, err
	}

	now := in.ObservedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	revKind := in.RevisionKind
	if revKind == "" {
		revKind = "pr_head"
	}
	externalID := fmt.Sprintf("taaro-awareness:%s/%s#%d:%s@%s", in.Owner, in.Repo, in.PRNumber, revKind, in.HeadSHA)
	output := &github.CheckRunOutput{
		Title:   github.Ptr(in.Title),
		Summary: github.Ptr(in.SummaryMarkdown),
	}

	var detailsURL *string
	if strings.HasPrefix(in.DetailsURL, "https://") {
		detailsURL = github.Ptr(in.DetailsURL)
	}

	var conclusionPtr *string
	var completedAt *github.Timestamp
	if status == "completed" {
		conclusionPtr = github.Ptr(conclusion)
		completedAt = &github.Timestamp{Time: now}
	}

	if in.ExistingCheckRun > 0 {
		updated, resp, err := cli.Checks.UpdateCheckRun(ctx, in.Owner, in.Repo, in.ExistingCheckRun, github.UpdateCheckRunOptions{
			Name:        AwarenessCheckName,
			DetailsURL:  detailsURL,
			ExternalID:  github.Ptr(externalID),
			Status:      github.Ptr(status),
			Conclusion:  conclusionPtr,
			CompletedAt: completedAt,
			Output:      output,
		})
		if err == nil {
			return updated.GetID(), nil
		}
		if resp == nil || resp.StatusCode != http.StatusNotFound {
			return 0, fmt.Errorf("update awareness check run: %w", err)
		}
	}

	created, _, err := cli.Checks.CreateCheckRun(ctx, in.Owner, in.Repo, github.CreateCheckRunOptions{
		Name:        AwarenessCheckName,
		HeadSHA:     in.HeadSHA,
		DetailsURL:  detailsURL,
		ExternalID:  github.Ptr(externalID),
		Status:      github.Ptr(status),
		Conclusion:  conclusionPtr,
		StartedAt:   &github.Timestamp{Time: now},
		CompletedAt: completedAt,
		Output:      output,
	})
	if err != nil {
		return 0, fmt.Errorf("create awareness check run for %s/%s@%s: %w", in.Owner, in.Repo, in.HeadSHA, err)
	}
	return created.GetID(), nil
}

// ReconcilePullRequest reads the current PR and its complete changed-file list.
// A missing or capped list is returned as unknown (nil), so it cannot clear a gate.
func (c *Client) ReconcilePullRequest(ctx context.Context, installationID int64, owner, repo string, number int) (store.PullRequestRecord, bool, error) {
	cli, err := c.forInstallation(installationID)
	if err != nil {
		return store.PullRequestRecord{}, false, err
	}
	pr, _, err := cli.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return store.PullRequestRecord{}, false, err
	}
	paths := []string{}
	fileCount := 0
	complete := pr.ChangedFiles != nil && pr.GetChangedFiles() < 3000
	if complete {
		for page := 1; ; {
			files, response, err := cli.PullRequests.ListFiles(ctx, owner, repo, number, &github.ListOptions{PerPage: 100, Page: page})
			if err != nil {
				return store.PullRequestRecord{}, false, err
			}
			fileCount += len(files)
			for _, file := range files {
				if file.GetFilename() == "" {
					complete = false
				}
				paths = append(paths, file.GetFilename())
				if file.GetPreviousFilename() != "" {
					paths = append(paths, file.GetPreviousFilename())
				}
			}
			if response.NextPage == 0 {
				break
			}
			if response.NextPage <= page {
				return store.PullRequestRecord{}, false, fmt.Errorf("invalid changed-file pagination")
			}
			page = response.NextPage
		}
		if fileCount != pr.GetChangedFiles() {
			complete = false
		}
	}
	if !complete {
		paths = nil
	}
	latest, _, err := cli.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return store.PullRequestRecord{}, false, err
	}
	if latest.GetHead().GetSHA() != pr.GetHead().GetSHA() || latest.GetBase().GetSHA() != pr.GetBase().GetSHA() || latest.GetChangedFiles() != pr.GetChangedFiles() {
		return store.PullRequestRecord{}, false, fmt.Errorf("pull request changed during reconciliation")
	}
	labels := []string{}
	for _, label := range latest.Labels {
		labels = append(labels, label.GetName())
	}
	return store.PullRequestRecord{RepoFullName: owner + "/" + repo, Number: number, Title: latest.GetTitle(), Body: latest.GetBody(), State: latest.GetState(), Draft: latest.GetDraft(), Merged: latest.GetMerged(), MergeCommitSHA: latest.GetMergeCommitSHA(), HTMLURL: latest.GetHTMLURL(), Author: latest.GetUser().GetLogin(), HeadSHA: latest.GetHead().GetSHA(), BaseRef: latest.GetBase().GetRef(), Labels: labels, Paths: paths, InstallationID: installationID, RevisionKind: "pr_head"}, latest.GetBase().GetRepo().GetPrivate(), nil
}

type IssueSnapshot struct {
	Title, Body, State, Owner, URL string
	Labels                         []string
	Private                        bool
}

// ReconcileIssue reads current issue state before indexing an issue webhook.
func (c *Client) ReconcileIssue(ctx context.Context, installationID int64, owner, repo string, number int) (IssueSnapshot, error) {
	cli, err := c.forInstallation(installationID)
	if err != nil {
		return IssueSnapshot{}, err
	}
	issue, _, err := cli.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	repository, _, err := cli.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return IssueSnapshot{}, err
	}
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}
	return IssueSnapshot{Title: issue.GetTitle(), Body: issue.GetBody(), Owner: issue.GetUser().GetLogin(), State: issue.GetState(), URL: issue.GetHTMLURL(), Labels: labels, Private: repository.GetPrivate()}, nil
}

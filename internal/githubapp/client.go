package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
)

const HandoffCheckName = "Taaro Handoff"

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

// HandoffPublisher publishes or updates a revision-anchored neutral Check Run.
type HandoffPublisher interface {
	PublishHandoffCheck(ctx context.Context, in HandoffCheckInput) (int64, error)
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

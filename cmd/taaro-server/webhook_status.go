package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
	_ "modernc.org/sqlite"
)

type pendingStatus struct {
	DeliveryID string `json:"delivery_id"`
	Event      string `json:"event"`
	Repository string `json:"repository"`
	ReceivedAt string `json:"received_at"`
	Attempts   int    `json:"attempts"`
	RetryAt    int64  `json:"retry_at_unix"`
}
type upstreamStatus struct {
	ID          int64  `json:"id"`
	GUID        string `json:"guid"`
	Event       string `json:"event"`
	Status      string `json:"status"`
	StatusCode  int    `json:"status_code"`
	DeliveredAt string `json:"delivered_at"`
	LocalRecord bool   `json:"local_record"`
}
type webhookStatus struct {
	RetryingCount     int              `json:"retrying_count"`
	OutstandingFailed int              `json:"outstanding_failed"`
	LookbackSince     string           `json:"lookback_since"`
	PendingCount      int              `json:"pending_count"`
	Pending           []pendingStatus  `json:"pending"`
	PendingTruncated  bool             `json:"pending_truncated"`
	Upstream          []upstreamStatus `json:"upstream,omitempty"`
	UpstreamChecked   bool             `json:"upstream_checked"`
	UpstreamError     string           `json:"upstream_error,omitempty"`
	UpstreamLimited   bool             `json:"upstream_limited"`
}

// inspectWebhooks opens SQLite read-only and performs only a GET upstream.
func inspectWebhooks(ctx context.Context, path string, cli *github.Client, since time.Time) (webhookStatus, error) {
	var result webhookStatus
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_webhooks`).Scan(&result.PendingCount); err != nil {
		return result, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_webhooks WHERE attempts > 0`).Scan(&result.RetryingCount); err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, `SELECT d.delivery_id,d.event,d.repo_full_name,d.received_at,p.attempts,p.retry_at FROM pending_webhooks p JOIN webhook_deliveries d USING(delivery_id) ORDER BY d.received_at LIMIT 100`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var p pendingStatus
		if err := rows.Scan(&p.DeliveryID, &p.Event, &p.Repository, &p.ReceivedAt, &p.Attempts, &p.RetryAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Pending = append(result.Pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	result.PendingTruncated = result.PendingCount > len(result.Pending)
	if cli == nil {
		return result, nil
	}
	result.LookbackSince = since.UTC().Format(time.RFC3339)
	latest := map[string]upstreamStatus{}
	latestAt := map[string]time.Time{}
	opts := &github.ListCursorOptions{PerPage: 100}
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page >= 100 {
			result.UpstreamLimited = true
			result.UpstreamError = "GitHub scan exceeded 100 pages"
			return result, errors.New(result.UpstreamError)
		}
		deliveries, response, err := cli.Apps.ListHookDeliveries(ctx, opts)
		if err != nil {
			result.UpstreamError = err.Error()
			return result, err
		}
		older := false
		for _, d := range deliveries {
			if d.DeliveredAt == nil || d.GetGUID() == "" {
				result.UpstreamLimited = true
				result.UpstreamError = "GitHub delivery missing timestamp or GUID"
				return result, errors.New(result.UpstreamError)
			}
			at := d.DeliveredAt.Time
			if at.Before(since) {
				older = true
				continue
			}
			var exists int
			if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhook_deliveries WHERE delivery_id=?)`, d.GetGUID()).Scan(&exists); err != nil {
				return result, err
			}
			entry := upstreamStatus{ID: d.GetID(), GUID: d.GetGUID(), Event: d.GetEvent(), Status: d.GetStatus(), StatusCode: d.GetStatusCode(), DeliveredAt: at.UTC().Format(time.RFC3339), LocalRecord: exists != 0}
			result.Upstream = append(result.Upstream, entry)
			prev, ok := latest[d.GetGUID()]
			if !ok || at.After(latestAt[d.GetGUID()]) || (at.Equal(latestAt[d.GetGUID()]) && entry.ID > prev.ID) {
				latest[d.GetGUID()] = entry
				latestAt[d.GetGUID()] = at
			}
		}
		if older || response == nil {
			break
		}
		next := response.Cursor
		if next == "" {
			next = response.NextPageToken
		}
		if next == "" && response.NextPage > 0 {
			next = fmt.Sprint(response.NextPage)
		}
		if next == "" {
			break
		}
		if seen[next] {
			result.UpstreamLimited = true
			result.UpstreamError = "GitHub pagination cursor repeated"
			return result, errors.New(result.UpstreamError)
		}
		seen[next] = true
		if response.Cursor != "" {
			opts.Cursor = next
		} else {
			opts.Page = next
		}
	}
	result.UpstreamChecked = true
	for _, d := range latest {
		if d.StatusCode < 200 || d.StatusCode >= 300 {
			result.OutstandingFailed++
		}
	}

	return result, nil
}

func printWebhookStatus(ctx context.Context, out io.Writer, path string, appID int64, keyPath string, localOnly bool, alert bool, since time.Time) error {
	var cli *github.Client
	if !localOnly {
		if appID <= 0 || keyPath == "" {
			return fmt.Errorf("GitHub App ID and private key are required; use -webhook-status-local-only for local queue only")
		}
		transport, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, appID, keyPath)
		if err != nil {
			return err
		}
		cli, err = github.NewClient(github.WithHTTPClient(&http.Client{Transport: transport, Timeout: 15 * time.Second}))
		if err != nil {
			return err
		}
	}
	report, err := inspectWebhooks(ctx, path, cli, since)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if writeErr := enc.Encode(report); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return err
	}
	if alert {
		return webhookAlertError(report)
	}
	return nil
}

func webhookAlertError(report webhookStatus) error {
	if report.RetryingCount > 0 || report.OutstandingFailed > 0 {
		return fmt.Errorf("webhook alert: %d retrying locally, %d failed upstream", report.RetryingCount, report.OutstandingFailed)
	}
	return nil
}

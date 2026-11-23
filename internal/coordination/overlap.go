package coordination

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
)

// OverlapSuggestion describes a detected overlap between two open/draft PRs in a repository.
// Every overlap is explicitly attributed to Taaro as a suggestion with match reason and dismissal control.
type OverlapSuggestion struct {
	RepoFullName    string    `json:"repo"`
	PRA             int       `json:"pr_a"`
	PRATitle        string    `json:"pr_a_title"`
	PRAURL          string    `json:"pr_a_url,omitempty"`
	PRB             int       `json:"pr_b"`
	PRBTitle        string    `json:"pr_b_title"`
	PRBURL          string    `json:"pr_b_url,omitempty"`
	MatchReasons    []string  `json:"match_reasons"`
	Source          string    `json:"source"` // "taaro"
	Authoritative   bool      `json:"authoritative"`
	Acknowledged    bool      `json:"acknowledged"`
	Disposition     string    `json:"disposition,omitempty"` // "complementary", "combined", "dismissed"
	AcknowledgedBy  string    `json:"acknowledged_by,omitempty"`
	AcknowledgedAt  time.Time `json:"acknowledged_at,omitempty"`
	SuppressedAlert bool      `json:"suppressed_alert"`
}

// DetectOverlaps compares open/draft PRs in repo using explicit issue links and declared
// proposal scopes, suppressing incidental lockfile-only collisions and honoring author acknowledgments.
func (e *Engine) DetectOverlaps(ctx context.Context, repoFullName string, filterPR int, includeAcknowledged bool) ([]OverlapSuggestion, error) {
	openPRs, err := e.Store.ListOpenPullRequests(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	proposals, err := e.Store.ListProposals(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	acks, err := e.Store.ListOverlapAcknowledgments(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	ackMap := make(map[[2]int]store.OverlapAcknowledgment, len(acks))
	for _, a := range acks {
		ackMap[[2]int{a.PRA, a.PRB}] = a
	}

	// Precompute active proposal keys matching each PR.
	prProposals := make(map[int][]string, len(openPRs))
	for _, pr := range openPRs {
		for _, p := range proposals {
			if p.State == "withdrawn" || p.State == "superseded" || p.Visibility == "private" {
				continue
			}
			if proposalMatchReason(p, pr.Paths, nil, pr.LinkedIssues) != "" {
				prProposals[pr.Number] = append(prProposals[pr.Number], p.ProposalKey)
			}
		}
	}

	var suggestions []OverlapSuggestion
	for i := 0; i < len(openPRs); i++ {
		for j := i + 1; j < len(openPRs); j++ {
			a, b := openPRs[i], openPRs[j]
			if a.Number > b.Number {
				a, b = b, a
			}
			if filterPR > 0 && a.Number != filterPR && b.Number != filterPR {
				continue
			}

			var reasons []string
			for _, iss := range SharedInts(a.LinkedIssues, b.LinkedIssues) {
				reasons = append(reasons, fmt.Sprintf("shared_issue:#%d", iss))
			}
			for _, pk := range SharedStrings(prProposals[a.Number], prProposals[b.Number]) {
				reasons = append(reasons, fmt.Sprintf("shared_proposal:%s", pk))
			}
			if len(reasons) == 0 {
				continue
			}

			s := OverlapSuggestion{
				RepoFullName:  repoFullName,
				PRA:           a.Number,
				PRATitle:      a.Title,
				PRAURL:        a.HTMLURL,
				PRB:           b.Number,
				PRBTitle:      b.Title,
				PRBURL:        b.HTMLURL,
				MatchReasons:  reasons,
				Source:        "taaro",
				Authoritative: false,
			}

			if ack, ok := ackMap[[2]int{a.Number, b.Number}]; ok {
				s.Acknowledged = true
				s.Disposition = ack.Disposition
				s.AcknowledgedBy = ack.Actor
				s.AcknowledgedAt = ack.AcknowledgedAt
				s.SuppressedAlert = true
				if !includeAcknowledged {
					continue
				}
			}

			suggestions = append(suggestions, s)
		}
	}

	slices.SortFunc(suggestions, func(x, y OverlapSuggestion) int {
		if x.PRA != y.PRA {
			return x.PRA - y.PRA
		}
		return x.PRB - y.PRB
	})
	return suggestions, nil
}

// AcknowledgeOverlap records an author's disposition ("complementary", "combined", or "dismissed")
// for an overlapping PR pair, suppressing repeated alerts on routine pushes.
func (e *Engine) AcknowledgeOverlap(ctx context.Context, repo string, prA, prB int, disposition, actor, note string) (store.OverlapAcknowledgment, error) {
	if repo == "" || prA <= 0 || prB <= 0 || prA == prB {
		return store.OverlapAcknowledgment{}, errors.New("repo and two distinct positive PR numbers are required")
	}
	disp := strings.ToLower(strings.TrimSpace(disposition))
	switch disp {
	case "complementary", "combined", "dismissed":
	default:
		return store.OverlapAcknowledgment{}, fmt.Errorf("disposition must be complementary, combined, or dismissed (got %q)", disposition)
	}
	if prA > prB {
		prA, prB = prB, prA
	}
	ack := store.OverlapAcknowledgment{
		RepoFullName:   repo,
		PRA:            prA,
		PRB:            prB,
		Disposition:    disp,
		Actor:          strings.TrimSpace(actor),
		Note:           strings.TrimSpace(note),
		AcknowledgedAt: e.now(),
	}
	if err := e.Store.UpsertOverlapAcknowledgment(ctx, ack); err != nil {
		return store.OverlapAcknowledgment{}, err
	}
	return ack, nil
}

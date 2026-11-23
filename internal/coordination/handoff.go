package coordination

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
)

// HandoffCard aggregates revision-anchored evidence for a PR with explicit provenance.
type HandoffCard struct {
	PullRequest      store.PullRequestRecord       `json:"pull_request"`
	Revisions        []store.PullRequestRevision   `json:"revisions,omitempty"`
	Reviews          []store.ReviewRecord          `json:"reviews,omitempty"`
	ExternalChecks   []store.ExternalCheckRun      `json:"external_checks,omitempty"`
	Artifacts        []store.ArtifactRecord        `json:"artifacts,omitempty"`
	ValidationChecks []store.ValidationCheckRecord `json:"validation_checks,omitempty"`
	Proposals        []EvidenceItem                `json:"proposals,omitempty"`
	Overlaps         []OverlapSuggestion           `json:"overlaps,omitempty"`
	Awareness        *store.AwarenessEvaluation    `json:"awareness,omitempty"`
	Actionable       bool                          `json:"actionable"`
	CopyableBrief    string                        `json:"copyable_brief"`
	SummaryMarkdown  string                        `json:"summary_markdown"`
	AssembledAt      time.Time                     `json:"assembled_at"`
}

// AssembleHandoff builds a revision-anchored HandoffCard for a pull request, honoring audience visibility.
func (e *Engine) AssembleHandoff(ctx context.Context, repoFullName string, prNumber int, audience string) (HandoffCard, error) {
	pr, err := e.Store.GetPullRequest(ctx, repoFullName, prNumber)
	if err != nil {
		return HandoffCard{}, err
	}

	revisions, err := e.Store.ListPullRequestRevisions(ctx, repoFullName, prNumber)
	if err != nil {
		return HandoffCard{}, err
	}
	reviews, err := e.Store.ListReviews(ctx, repoFullName, prNumber)
	if err != nil {
		return HandoffCard{}, err
	}
	extChecks, err := e.Store.ListExternalCheckRuns(ctx, repoFullName, pr.HeadSHA)
	if err != nil {
		return HandoffCard{}, err
	}
	allArtifacts, err := e.Store.ListLatestArtifacts(ctx, repoFullName)
	if err != nil {
		return HandoffCard{}, err
	}
	valChecks, err := e.Store.ListValidationChecks(ctx, repoFullName, prNumber)
	if err != nil {
		return HandoffCard{}, err
	}
	proposals, err := e.Store.ListProposals(ctx, repoFullName)
	if err != nil {
		return HandoffCard{}, err
	}
	overlaps, err := e.DetectOverlaps(ctx, repoFullName, prNumber, false)
	if err != nil {
		return HandoffCard{}, err
	}

	var awareness *store.AwarenessEvaluation
	if ev, err := e.Store.GetLatestAwarenessEvaluation(ctx, repoFullName, prNumber, pr.HeadSHA); err == nil {
		awareness = &ev
	} else if !errors.Is(err, store.ErrNotFound) {
		return HandoffCard{}, err
	}

	// Filter artifacts associated with this PR or produced from its head SHA.
	var prArtifacts []store.ArtifactRecord
	for _, a := range allArtifacts {
		if a.PRNumber == prNumber || (pr.HeadSHA != "" && a.ProducerRev == pr.HeadSHA) {
			prArtifacts = append(prArtifacts, a)
		}
	}

	aud := normalizeAudience(audience)
	var matchedProposals []EvidenceItem
	privateMatch := false
	for _, p := range proposals {
		if p.State == "withdrawn" || p.State == "superseded" {
			continue
		}
		reason := proposalMatchReason(p, pr.Paths, nil, pr.LinkedIssues)
		if reason == "" {
			continue
		}
		if p.Visibility != "public" && aud != "authorized" {
			privateMatch = true
			continue
		}
		auth := "taaro_suggestion"
		if p.State == "accepted" {
			auth = "authoritative_source"
		}
		matchedProposals = append(matchedProposals, EvidenceItem{
			Kind:           "proposal",
			Key:            p.ProposalKey,
			Version:        fmt.Sprintf("v%d", p.Version),
			LifecycleState: p.State,
			Title:          p.Title,
			Summary:        p.Summary,
			QuotedExcerpt:  p.Excerpt,
			QuotedData:     true,
			SourceLink:     p.SourceURL,
			MatchReason:    reason,
			Authority:      auth,
			ObservedAt:     p.ObservedAt,
		})
	}

	if privateMatch {
		matchedProposals = append(matchedProposals, EvidenceItem{Kind: "proposal", QuotedData: true, MatchReason: "private_source_match", Authority: "authoritative_source", Redacted: true})
	}

	hasUnresolvedReview := false
	seenReviewers := map[string]bool{}
	for _, r := range reviews {
		if seenReviewers[r.Reviewer] {
			continue
		}
		seenReviewers[r.Reviewer] = true
		if strings.EqualFold(r.State, "CHANGES_REQUESTED") && r.CommitSHA == pr.HeadSHA && pr.HeadSHA != "" {
			hasUnresolvedReview = true
		}
	}
	hasValidation := false
	for _, v := range valChecks {
		if v.SupersededByID == 0 {
			hasValidation = true
			break
		}
	}
	actionable := len(matchedProposals) > 0 || len(overlaps) > 0 || len(prArtifacts) > 0 || hasValidation || hasUnresolvedReview

	card := HandoffCard{
		PullRequest:      pr,
		Revisions:        revisions,
		Reviews:          reviews,
		ExternalChecks:   extChecks,
		Artifacts:        prArtifacts,
		ValidationChecks: valChecks,
		Proposals:        matchedProposals,
		Overlaps:         overlaps,
		Awareness:        awareness,
		Actionable:       actionable,
		AssembledAt:      e.now(),
	}
	card.SummaryMarkdown = RenderHandoffMarkdown(card)
	card.CopyableBrief = RenderCopyableBrief(card)
	return card, nil
}

// RenderHandoffMarkdown produces a concise, one-screen Markdown summary anchored to head_sha.
func RenderHandoffMarkdown(card HandoffCard) string {
	pr := card.PullRequest
	var b strings.Builder
	// All external text is displayed inside bounded code spans. Backticks and line
	// breaks are replaced so content cannot create Markdown, mentions, or links.
	q := safeHandoffText
	fmt.Fprintf(&b, "**PR #%d** (`%s`) observed at `%s` via `%s`.\n\n", pr.Number, q(pr.Title), pr.ObservedAt.UTC().Format(time.RFC3339), q(pr.LastAction))
	fmt.Fprintf(&b, "- **Base branch:** `%s`\n- **Head SHA:** `%s` (`%s`)\n- **Draft:** `%t`\n", q(pr.BaseRef), q(shortSHA(pr.HeadSHA)), q(fallbackStr(pr.RevisionKind, "pr_head")), pr.Draft)
	if pr.Merged && pr.MergeCommitSHA != "" {
		fmt.Fprintf(&b, "- **Merge commit:** `%s` (GitHub merge association)\n", q(shortSHA(pr.MergeCommitSHA)))
	}
	fmt.Fprintf(&b, "- **CLI query:** `taaro context --repo %s --pr %d`\n", q(pr.RepoFullName), pr.Number)
	if len(card.Reviews) > 0 {
		r := card.Reviews[0]
		note := " (reviewed revision unknown)"
		if r.CommitSHA != "" {
			if r.CommitSHA == pr.HeadSHA {
				note = " (current head)"
			} else {
				note = " (earlier revision; current head has changed)"
			}
		}
		fmt.Fprintf(&b, "\n### Latest recorded review (GitHub observation)\n- `%s` by `%s` on `%s`%s\n", q(r.State), q(r.Reviewer), q(shortSHA(r.CommitSHA)), note)
	}
	if len(card.ExternalChecks) > 0 {
		b.WriteString("\n### Recorded checks on current SHA\n")
		for i, c := range card.ExternalChecks {
			if i >= 10 {
				b.WriteString("- Additional checks omitted.\n")
				break
			}
			verdict := c.Status
			if c.Conclusion != "" {
				verdict = c.Conclusion
			}
			fmt.Fprintf(&b, "- `%s` at `%s`: `%s`\n", q(c.Name), q(shortSHA(c.HeadSHA)), q(verdict))
		}
	}
	if len(card.Artifacts) > 0 {
		b.WriteString("\n### Published fixtures\n")
		for i, a := range card.Artifacts {
			if i >= 10 {
				b.WriteString("- Additional fixtures omitted.\n")
				break
			}
			fmt.Fprintf(&b, "- `%s` **v%d** (producer rev `%s`, worker `%s`)", q(a.ArtifactKey), a.Version, q(shortSHA(a.ProducerRev)), q(a.ProducerWorker))
			if a.Command != "" {
				fmt.Fprintf(&b, " — rerun: `%s`", q(a.Command))
			}
			b.WriteString("\n")
		}
	}
	if len(card.ValidationChecks) > 0 {
		b.WriteString("\n### Recorded validations (job observations)\n")
		shown := 0
		for _, v := range card.ValidationChecks {
			if v.SupersededByID > 0 {
				continue
			}
			if shown >= 10 {
				b.WriteString("- Additional validations omitted.\n")
				break
			}
			shown++
			verdict := v.Status
			if v.Conclusion != "" {
				verdict = v.Conclusion
			}
			note := " (does not identify the current head)"
			if v.ProducerRev == pr.HeadSHA || v.ConsumerRev == pr.HeadSHA {
				note = " (includes current head)"
			}
			fmt.Fprintf(&b, "- `%s`: `%s` (producer `%s` + consumer `%s`)%s\n", q(v.JobName), q(verdict), q(shortSHA(v.ProducerRev)), q(fallbackStr(shortSHA(v.ConsumerRev), "n/a")), note)
		}
	}
	if len(card.Proposals) > 0 {
		b.WriteString("\n### Applicable proposals\n")
		for i, p := range card.Proposals {
			if i >= 10 {
				b.WriteString("- Additional proposals omitted.\n")
				break
			}
			if p.Redacted {
				b.WriteString("- Private source match; authorized review required.\n")
				continue
			}
			fmt.Fprintf(&b, "- `%s` `%s` (`%s`, `%s`, `%s`): `%s`\n", q(p.Key), q(p.Version), q(p.LifecycleState), q(p.MatchReason), q(p.Authority), q(p.Title))
		}
	}
	if len(card.Overlaps) > 0 {
		b.WriteString("\n### Related pull requests (Taaro suggestion)\n")
		for i, o := range card.Overlaps {
			if i >= 10 {
				b.WriteString("- Additional suggestions omitted.\n")
				break
			}
			n, title := o.PRB, o.PRBTitle
			if o.PRB == pr.Number {
				n, title = o.PRA, o.PRATitle
			}
			fmt.Fprintf(&b, "- PR #%d (`%s`) — matched via `%s`\n", n, q(title), q(strings.Join(o.MatchReasons, ", ")))
		}
	}
	return b.String()
}

// A short code span cannot escape into surrounding Markdown or trigger mentions.
func safeHandoffText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '`' {
			return '\''
		}
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:157]) + "..."
	}
	return value
}

// RenderCopyableBrief generates a compact text brief suitable for pasting into a fresh worker session.
func RenderCopyableBrief(card HandoffCard) string {
	pr := card.PullRequest
	var lines []string
	lines = append(lines, fmt.Sprintf("Taaro Handoff Brief — %s #%d (%s)", safeHandoffText(pr.RepoFullName), pr.Number, safeHandoffText(pr.Title)))
	lines = append(lines, fmt.Sprintf("Head: %s | Base: %s | State: %s (draft=%t) | Observed: %s",
		safeHandoffText(shortSHA(pr.HeadSHA)), safeHandoffText(pr.BaseRef), safeHandoffText(pr.State), pr.Draft, pr.ObservedAt.UTC().Format(time.RFC3339)))
	if len(card.Artifacts) > 0 {
		a := card.Artifacts[0]
		lines = append(lines, fmt.Sprintf("Fixture: %s v%d (rev=%s, cmd=%q)", safeHandoffText(a.ArtifactKey), a.Version, safeHandoffText(shortSHA(a.ProducerRev)), safeHandoffText(a.Command)))
	}
	if len(card.Proposals) > 0 {
		lines = append(lines, fmt.Sprintf("Proposals: %d applicable", len(card.Proposals)))
	}
	if len(card.Overlaps) > 0 {
		lines = append(lines, fmt.Sprintf("Overlaps: %d active Taaro suggestion(s)", len(card.Overlaps)))
	}
	lines = append(lines, fmt.Sprintf("Inspect: taaro context --repo %s --pr %d --full", safeHandoffText(pr.RepoFullName), pr.Number))
	return strings.Join(lines, "\n")
}

func fallbackStr(v, dflt string) string {
	if strings.TrimSpace(v) == "" {
		return dflt
	}
	return v
}

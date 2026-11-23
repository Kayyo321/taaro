package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIBodyBytes+1))
	if err != nil {
		http.Error(w, "failed reading request body", http.StatusBadRequest)
		return false
	}
	if len(body) > maxAPIBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON body: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) handlePublishArtifact(w http.ResponseWriter, r *http.Request) {
	var req store.ArtifactRecord
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RepoFullName) == "" || strings.TrimSpace(req.ArtifactKey) == "" || strings.TrimSpace(req.ProducerRev) == "" {
		http.Error(w, "repo, artifact_key, and producer_rev are required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ProducerWorker) == "" {
		req.ProducerWorker = "anonymous-producer"
	}

	art, stale, err := s.Store.PublishArtifact(r.Context(), req)
	if err != nil {
		http.Error(w, fmt.Sprintf("publish artifact failed: %v", err), http.StatusInternalServerError)
		return
	}
	if s.WebhookHandler != nil {
		s.WebhookHandler.Drain()
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"artifact":        art,
		"stale_consumers": stale,
	})
}

func (s *Server) handleGetOrListArtifacts(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		http.Error(w, "query parameter 'repo' is required", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		list, err := s.Store.ListLatestArtifacts(r.Context(), repo)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": list})
		return
	}

	ver := 0
	if rawVer := strings.TrimSpace(r.URL.Query().Get("version")); rawVer != "" {
		n, err := strconv.Atoi(rawVer)
		if err != nil || n <= 0 {
			http.Error(w, "version must be a positive integer", http.StatusBadRequest)
			return
		}
		ver = n
	}

	art, err := s.Store.GetArtifact(r.Context(), repo, key, ver)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, fmt.Sprintf("artifact %s/%s not found", repo, key), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, art)
}

func (s *Server) handleSubscribeArtifact(w http.ResponseWriter, r *http.Request) {
	var req store.ArtifactSubscription
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RepoFullName) == "" || strings.TrimSpace(req.ArtifactKey) == "" ||
		strings.TrimSpace(req.WorkerID) == "" || req.ContextGeneration <= 0 {
		http.Error(w, "repo, artifact_key, worker_id, and positive context_generation are required", http.StatusBadRequest)
		return
	}

	sub, err := s.Store.UpsertArtifactSubscription(r.Context(), req)
	if errors.Is(err, store.ErrInvalidSubscription) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("subscribe artifact failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleListArtifactConsumers(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if repo == "" || key == "" {
		http.Error(w, "query parameters 'repo' and 'key' are required", http.StatusBadRequest)
		return
	}
	consumers, err := s.Store.ListArtifactConsumers(r.Context(), repo, key)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"consumers": consumers})
}

func (s *Server) handleRecordCheck(w http.ResponseWriter, r *http.Request) {
	var req store.ValidationCheckRecord
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rec, cancelled, err := s.engine().RecordValidationCheck(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.WebhookHandler != nil {
		s.WebhookHandler.Drain()
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"check":         rec,
		"cancelled_ids": cancelled,
	})
}

func (s *Server) handleListChecks(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		http.Error(w, "query parameter 'repo' is required", http.StatusBadRequest)
		return
	}
	prNum := 0
	if rawPR := strings.TrimSpace(r.URL.Query().Get("pr")); rawPR != "" {
		n, err := strconv.Atoi(rawPR)
		if err != nil || n <= 0 {
			http.Error(w, "pr must be a positive integer", http.StatusBadRequest)
			return
		}
		prNum = n
	}
	checks, err := s.Store.ListValidationChecks(r.Context(), repo, prNum)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (s *Server) handleMatchCheck(w http.ResponseWriter, r *http.Request) {
	var req coordination.MatchCheckRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	resp, err := s.engine().MatchEligibleCheck(r.Context(), req)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpsertProposal(w http.ResponseWriter, r *http.Request) {
	var req store.ProposalRecord
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RepoFullName) == "" || strings.TrimSpace(req.ProposalKey) == "" || strings.TrimSpace(req.Title) == "" {
		http.Error(w, "repo, proposal_key, and title are required", http.StatusBadRequest)
		return
	}
	if req.SourceType == "" {
		req.SourceType = "rfc"
	}
	if req.SourceRef == "" {
		req.SourceRef = req.ProposalKey
	}
	switch req.State {
	case "", "draft", "accepted", "withdrawn", "superseded":
	default:
		http.Error(w, "state must be draft, accepted, withdrawn, or superseded", http.StatusBadRequest)
		return
	}
	switch req.Visibility {
	case "", "public", "private":
	default:
		http.Error(w, "visibility must be public or private", http.StatusBadRequest)
		return
	}

	rec, err := s.Store.UpsertProposal(r.Context(), req)
	if err != nil {
		http.Error(w, fmt.Sprintf("upsert proposal failed: %v", err), http.StatusInternalServerError)
		return
	}
	if s.WebhookHandler != nil {
		s.WebhookHandler.Drain()
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		http.Error(w, "query parameter 'repo' is required", http.StatusBadRequest)
		return
	}
	list, err := s.Store.ListProposals(r.Context(), repo)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if s.requestAudience(r) == "public" {
		filtered := make([]store.ProposalRecord, 0, len(list))
		for _, p := range list {
			if p.Visibility == "private" {
				// Never leak private titles, paths, or excerpts to public readers.
				filtered = append(filtered, store.ProposalRecord{
					RepoFullName: p.RepoFullName,
					ProposalKey:  "private:redacted",
					SourceType:   p.SourceType,
					State:        p.State,
					Version:      p.Version,
					Visibility:   "private",
					ObservedAt:   p.ObservedAt,
				})
				continue
			}
			filtered = append(filtered, p)
		}
		list = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": list})
}

func (s *Server) handleRegisterScope(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepoFullName      string   `json:"repo"`
		WorkerID          string   `json:"worker_id"`
		ContextGeneration int      `json:"context_generation"`
		ParentWorkerID    string   `json:"parent_worker_id,omitempty"`
		Role              string   `json:"role,omitempty"`
		PRNumber          int      `json:"pr_number,omitempty"`
		BaseRev           string   `json:"base_rev,omitempty"`
		IssueLinks        []int    `json:"issue_links,omitempty"`
		Paths             []string `json:"paths,omitempty"`
		Interfaces        []string `json:"interfaces,omitempty"`
		TTLSeconds        int      `json:"ttl_seconds,omitempty"`
		Audience          string   `json:"audience,omitempty"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	aud := body.Audience
	if aud == "" || s.requestAudience(r) == "public" {
		aud = s.requestAudience(r)
	}
	resp, err := s.engine().RegisterScope(r.Context(), coordination.RegisterScopeRequest{
		RepoFullName:      body.RepoFullName,
		WorkerID:          body.WorkerID,
		ContextGeneration: body.ContextGeneration,
		ParentWorkerID:    body.ParentWorkerID,
		Role:              body.Role,
		PRNumber:          body.PRNumber,
		BaseRev:           body.BaseRev,
		IssueLinks:        body.IssueLinks,
		Paths:             body.Paths,
		Interfaces:        body.Interfaces,
		TTL:               time.Duration(body.TTLSeconds) * time.Second,
		Audience:          aud,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleEvaluateAction(w http.ResponseWriter, r *http.Request) {
	var req coordination.EvaluateActionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Audience == "" || s.requestAudience(r) == "public" {
		req.Audience = s.requestAudience(r)
	}
	resp, err := s.engine().EvaluateAction(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRecordReceipt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoFullName      string              `json:"repo"`
		WorkerID          string              `json:"worker_id"`
		ContextGeneration int                 `json:"context_generation"`
		Items             []store.ReceiptItem `json:"items"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rec, err := s.engine().RecordReceipt(r.Context(), req.RepoFullName, req.WorkerID, req.ContextGeneration, req.Items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) handleVerifyCandidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		coordination.CandidateAwarenessRequest
		Publish bool `json:"publish,omitempty"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Audience == "" || s.requestAudience(r) == "public" {
		req.Audience = s.requestAudience(r)
	}
	engine := s.engine()
	var pr store.PullRequestRecord
	if req.Publish {
		engine.AwarenessPublication.Lock()
		defer engine.AwarenessPublication.Unlock()
		if s.AwarenessPublisher == nil {
			http.Error(w, "GitHub awareness publication is disabled", http.StatusServiceUnavailable)
			return
		}
		if req.PRNumber <= 0 || (req.RevisionKind != "" && req.RevisionKind != "pr_head") {
			http.Error(w, "API publication requires an observed PR head", http.StatusBadRequest)
			return
		}
		var err error
		pr, err = s.Store.GetPullRequest(r.Context(), req.RepoFullName, req.PRNumber)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown pull request", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "cannot read pull request", http.StatusInternalServerError)
			return
		}
		if pr.HeadSHA != req.HeadSHA || pr.InstallationID <= 0 || pr.State != "open" {
			http.Error(w, "candidate must match an installed, open pull request head", http.StatusConflict)
			return
		}
		req.TargetPaths = pr.Paths
		req.LinkedIssues = pr.LinkedIssues
		req.Interfaces = nil
		req.Audience = "public"
	}
	resp, err := engine.EvaluateCandidate(r.Context(), req.CandidateAwarenessRequest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Publish {
		var existingID int64
		if pr.AwarenessCheckHead == req.HeadSHA {
			existingID = pr.AwarenessCheckRunID
		}
		owner, repo, ok := strings.Cut(req.RepoFullName, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			http.Error(w, "invalid repository name", http.StatusBadRequest)
			return
		}
		status := "completed"
		conclusion := resp.Evaluation.Verdict
		if conclusion == "in_progress" {
			status = "in_progress"
			conclusion = ""
		}
		detailsURL := ""
		if s.PublicURL != "" && req.PRNumber > 0 {
			detailsURL = fmt.Sprintf("%s/handoffs/%s/%s/pull/%d?sha=%s",
				strings.TrimRight(s.PublicURL, "/"), owner, repo, req.PRNumber, url.QueryEscape(req.HeadSHA))
		}
		crID, pubErr := s.AwarenessPublisher.PublishAwarenessCheck(r.Context(), githubapp.AwarenessCheckInput{
			InstallationID:   pr.InstallationID,
			Owner:            owner,
			Repo:             repo,
			PRNumber:         req.PRNumber,
			HeadSHA:          req.HeadSHA,
			RevisionKind:     resp.Evaluation.RevisionKind,
			Status:           status,
			Conclusion:       conclusion,
			Title:            fmt.Sprintf("Awareness (%s): %s", resp.Evaluation.RevisionKind, resp.Evaluation.Verdict),
			SummaryMarkdown:  resp.Evaluation.Reason,
			DetailsURL:       detailsURL,
			ExistingCheckRun: existingID,
			ObservedAt:       resp.Evaluation.ValidatedAt,
		})
		if pubErr != nil {
			http.Error(w, "GitHub rejected awareness check publication", http.StatusBadGateway)
			return
		}
		resp.Evaluation.CheckRunID = crID
		if err := engine.RecordAwarenessPublication(r.Context(), resp.Evaluation, crID); err != nil {
			http.Error(w, "check published but its ID could not be recorded", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListOverlaps(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		http.Error(w, "query parameter 'repo' is required", http.StatusBadRequest)
		return
	}
	filterPR := 0
	if rawPR := strings.TrimSpace(r.URL.Query().Get("pr")); rawPR != "" {
		n, err := strconv.Atoi(rawPR)
		if err != nil || n <= 0 {
			http.Error(w, "pr must be a positive integer", http.StatusBadRequest)
			return
		}
		filterPR = n
	}
	includeAck := r.URL.Query().Get("include_acknowledged") == "1" || r.URL.Query().Get("include_acknowledged") == "true"

	overlaps, err := s.engine().DetectOverlaps(r.Context(), repo, filterPR, includeAck)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overlaps": overlaps})
}

func (s *Server) handleAckOverlap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoFullName string `json:"repo"`
		PRA          int    `json:"pr_a"`
		PRB          int    `json:"pr_b"`
		Disposition  string `json:"disposition"`
		Actor        string `json:"actor,omitempty"`
		Note         string `json:"note,omitempty"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	ack, err := s.engine().AcknowledgeOverlap(r.Context(), req.RepoFullName, req.PRA, req.PRB, req.Disposition, req.Actor, req.Note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

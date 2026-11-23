package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kayyo321/taaro/internal/config"
	"github.com/Kayyo321/taaro/internal/coordination"
	"github.com/Kayyo321/taaro/internal/githubapp"
	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/version"
	"github.com/Kayyo321/taaro/internal/webhook"
)

const maxAPIBodyBytes = 4 << 20 // 4 MiB

// Server wires HTTP endpoints for the Taaro GitHub App and Coordination API v1.
type Server struct {
	engineOnce         sync.Once
	AppID              int64
	PublicURL          string
	APIToken           string
	Repository         string
	Store              store.Store
	Engine             *coordination.Engine
	WebhookHandler     *webhook.Handler
	AwarenessPublisher githubapp.AwarenessPublisher
	Logger             *slog.Logger
}

func (s *Server) engine() *coordination.Engine {
	s.engineOnce.Do(func() {
		if s.Engine == nil {
			s.Engine = &coordination.Engine{Store: s.Store}
		}
	})
	return s.Engine
}

// Handler returns the configured HTTP router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	if s.WebhookHandler != nil {
		mux.Handle("POST /webhooks/github", s.WebhookHandler)
	}
	mux.HandleFunc("GET /handoffs/{owner}/{repo}/pull/{number}", s.withAPIAuth(s.handleHandoffPage))

	// Coordination API v1 (optionally protected by TAARO_API_TOKEN).
	mux.HandleFunc("GET /api/v1/context", s.withAPIAuth(s.handleContext))
	mux.HandleFunc("GET /api/v1/handoff", s.withAPIAuth(s.handleHandoffJSON))

	// Level 2: Immutable Artifacts & Subscriptions.
	mux.HandleFunc("POST /api/v1/artifacts", s.withAPIAuth(s.handlePublishArtifact))
	mux.HandleFunc("GET /api/v1/artifacts", s.withAPIAuth(s.handleGetOrListArtifacts))
	mux.HandleFunc("POST /api/v1/artifacts/subscribe", s.withAPIAuth(s.handleSubscribeArtifact))
	mux.HandleFunc("GET /api/v1/artifacts/consumers", s.withAPIAuth(s.handleListArtifactConsumers))

	// Level 2: Check Identity & Combined Validation.
	mux.HandleFunc("POST /api/v1/checks", s.withAPIAuth(s.handleRecordCheck))
	mux.HandleFunc("GET /api/v1/checks", s.withAPIAuth(s.handleListChecks))
	mux.HandleFunc("POST /api/v1/checks/match", s.withAPIAuth(s.handleMatchCheck))

	// Level 1 & 3: Proposals, Scopes, Awareness Mediation, Delivery Receipts, and Overlaps.
	mux.HandleFunc("POST /api/v1/proposals", s.withAPIAuth(s.handleUpsertProposal))
	mux.HandleFunc("GET /api/v1/proposals", s.withAPIAuth(s.handleListProposals))
	mux.HandleFunc("POST /api/v1/scopes", s.withAPIAuth(s.handleRegisterScope))
	mux.HandleFunc("POST /api/v1/awareness/evaluate", s.withAPIAuth(s.handleEvaluateAction))
	mux.HandleFunc("POST /api/v1/awareness/receipt", s.withAPIAuth(s.handleRecordReceipt))
	mux.HandleFunc("POST /api/v1/awareness/verify-pr", s.withAPIAuth(s.handleVerifyCandidate))
	mux.HandleFunc("GET /api/v1/overlaps", s.withAPIAuth(s.handleListOverlaps))
	mux.HandleFunc("POST /api/v1/overlaps/ack", s.withAPIAuth(s.handleAckOverlap))

	return s.logMiddleware(http.NewCrossOriginProtection().Handler(mux))
}

func (s *Server) withAPIAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.APIToken == "" {
			if !config.DirectLoopbackRequest(r) {
				http.Error(w, "tokenless access requires a direct loopback connection", http.StatusForbidden)
				return
			}
		} else {
			got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") ||
				subtle.ConstantTimeCompare([]byte(got), []byte(s.APIToken)) != 1 {
				http.Error(w, "unauthorized: valid Bearer token required", http.StatusUnauthorized)
				return
			}
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && s.Repository != "" {
			repo := strings.TrimSpace(r.URL.Query().Get("repo"))
			if owner := r.PathValue("owner"); owner != "" {
				repo = owner + "/" + r.PathValue("repo")
			}
			if !s.allowRepository(w, repo) {
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) allowRepository(w http.ResponseWriter, repo string) bool {
	if s.Repository == "" || repo == s.Repository {
		return true
	}
	if repo == "" {
		http.Error(w, "repository is required", http.StatusBadRequest)
	} else {
		http.Error(w, "repository is outside this instance's scope", http.StatusNotFound)
	}
	return false
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"version":          version.Version,
		"protocol_version": version.ProtocolVersion,
		"app_id":           s.AppID,
	})
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	rawPR := strings.TrimSpace(r.URL.Query().Get("pr"))
	if repo == "" || rawPR == "" {
		http.Error(w, "query parameters 'repo' (owner/repo) and 'pr' (number) are required", http.StatusBadRequest)
		return
	}

	prNum, err := strconv.Atoi(rawPR)
	if err != nil || prNum <= 0 {
		http.Error(w, "query parameter 'pr' must be a positive integer", http.StatusBadRequest)
		return
	}

	rec, err := s.Store.GetPullRequest(r.Context(), repo, prNum)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, fmt.Sprintf("no observed context for %s#%d", repo, prNum), http.StatusNotFound)
		return
	}
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("failed fetching PR context", "repo", repo, "pr", prNum, "err", err)
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleHandoffJSON(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	rawPR := strings.TrimSpace(r.URL.Query().Get("pr"))
	if repo == "" || rawPR == "" {
		http.Error(w, "query parameters 'repo' (owner/repo) and 'pr' (number) are required", http.StatusBadRequest)
		return
	}
	prNum, err := strconv.Atoi(rawPR)
	if err != nil || prNum <= 0 {
		http.Error(w, "query parameter 'pr' must be a positive integer", http.StatusBadRequest)
		return
	}

	card, err := s.engine().AssembleHandoff(r.Context(), repo, prNum, s.requestAudience(r))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, fmt.Sprintf("no observed handoff for %s#%d", repo, prNum), http.StatusNotFound)
		return
	}
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("failed assembling handoff", "repo", repo, "pr", prNum, "err", err)
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) handleHandoffPage(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	rawNum := r.PathValue("number")
	prNum, err := strconv.Atoi(rawNum)
	if err != nil || prNum <= 0 {
		http.Error(w, "invalid pull request number", http.StatusBadRequest)
		return
	}

	fullName := owner + "/" + repo
	card, err := s.engine().AssembleHandoff(r.Context(), fullName, prNum, s.requestAudience(r))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if sha := r.URL.Query().Get("sha"); sha != "" && sha != card.PullRequest.HeadSHA {
		http.Error(w, "this revision is no longer available locally", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handoffPageTmpl.Execute(w, card)
}

func (s *Server) requestAudience(r *http.Request) string {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("audience")), "public") {
		return "public"
	}
	return "authorized"
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if s.Logger != nil {
			s.Logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var handoffPageTmpl = template.Must(template.New("handoff").Funcs(template.FuncMap{
	"short": func(s string) string {
		if len(s) > 7 {
			return s[:7]
		}
		return s
	},
	"join": strings.Join,
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Taaro Handoff — {{.PullRequest.RepoFullName}}#{{.PullRequest.Number}}</title>
  <style>
    body { font-family: system-ui, -apple-system, sans-serif; max-width: 820px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; color: #18181b; }
    h1 { font-size: 1.4rem; margin-bottom: 0.25rem; }
    h2 { font-size: 1.05rem; margin-top: 1.5rem; border-bottom: 1px solid #e4e4e7; padding-bottom: 0.25rem; }
    code { background: #f4f4f5; padding: 0.15rem 0.35rem; border-radius: 4px; font-size: 0.9em; }
    pre { background: #f4f4f5; padding: 0.75rem; border-radius: 6px; overflow-x: auto; font-size: 0.85em; }
    .meta { color: #52525b; font-size: 0.92rem; }
    .badge { display: inline-block; padding: 0.1rem 0.45rem; border-radius: 999px; font-size: 0.8rem; font-weight: 600; background: #e4e4e7; }
    ul { padding-left: 1.25rem; }
  </style>
</head>
<body>
  <h1>{{.PullRequest.RepoFullName}} #{{.PullRequest.Number}}: {{.PullRequest.Title}}</h1>
  <p class="meta">
    <span class="badge">{{.PullRequest.State}}{{if .PullRequest.Draft}} / draft{{end}}</span>
    Head <code>{{short .PullRequest.HeadSHA}}</code> onto <code>{{.PullRequest.BaseRef}}</code>
    · Last action <code>{{.PullRequest.LastAction}}</code>
    · Observed <code>{{.PullRequest.ObservedAt.Format "2006-01-02T15:04:05Z07:00"}}</code>
    {{if .PullRequest.HTMLURL}}· <a href="{{.PullRequest.HTMLURL}}" target="_blank" rel="noreferrer">GitHub PR</a>{{end}}
  </p>

  {{if .Awareness}}
  <h2>Required Awareness Check</h2>
  <p>Verdict: <strong>{{.Awareness.Verdict}}</strong> (complete={{.Awareness.Complete}}, revision kind=<code>{{.Awareness.RevisionKind}}</code>) — {{.Awareness.Reason}}</p>
  {{end}}

  {{if .Reviews}}
  <h2>Reviews (GitHub platform observation)</h2>
  <ul>
    {{range .Reviews}}
    <li><strong>{{.State}}</strong> by <code>@{{.Reviewer}}</code> on <code>{{short .CommitSHA}}</code>{{if .BodyExcerpt}} — {{.BodyExcerpt}}{{end}}</li>
    {{end}}
  </ul>
  {{end}}

  {{if .ExternalChecks}}
  <h2>Checks on Head SHA</h2>
  <ul>
    {{range .ExternalChecks}}
    <li><code>{{.Name}}</code>: <strong>{{if .Conclusion}}{{.Conclusion}}{{else}}{{.Status}}{{end}}</strong></li>
    {{end}}
  </ul>
  {{end}}

  {{if .Artifacts}}
  <h2>Published Fixtures</h2>
  <ul>
    {{range .Artifacts}}
    <li><code>{{.ArtifactKey}}</code> <strong>v{{.Version}}</strong> from <code>{{short .ProducerRev}}</code> ({{.ProducerWorker}}){{if .Command}} — rerun: <code>{{.Command}}</code>{{end}}</li>
    {{end}}
  </ul>
  {{end}}

  {{if .ValidationChecks}}
  <h2>Combined Validations</h2>
  <ul>
    {{range .ValidationChecks}}
    <li><code>{{.JobName}}</code>: <strong>{{if .Conclusion}}{{.Conclusion}}{{else}}{{.Status}}{{end}}</strong> (producer <code>{{short .ProducerRev}}</code> + consumer <code>{{short .ConsumerRev}}</code>)</li>
    {{end}}
  </ul>
  {{end}}

  {{if .Proposals}}
  <h2>Applicable Proposals</h2>
  <ul>
    {{range .Proposals}}
    {{if .Redacted}}
    <li><code>{{.Key}}</code> ({{.LifecycleState}}) — authorized review required (private contents withheld)</li>
    {{else}}
    <li><code>{{.Key}}</code> <strong>{{.Version}}</strong> ({{.LifecycleState}}, {{.MatchReason}}): {{.Title}}</li>
    {{end}}
    {{end}}
  </ul>
  {{end}}

  {{if .Overlaps}}
  <h2>Related Pull Requests (Taaro suggestion)</h2>
  <ul>
    {{range .Overlaps}}
    <li>PR #{{.PRA}} &amp; PR #{{.PRB}} — matched via {{join .MatchReasons ", "}}</li>
    {{end}}
  </ul>
  {{end}}

  <h2>Copyable Brief</h2>
  <pre>{{.CopyableBrief}}</pre>
</body>
</html>`))

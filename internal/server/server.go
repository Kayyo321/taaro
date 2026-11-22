package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
	"github.com/Kayyo321/taaro/internal/webhook"
)

// Server wires HTTP endpoints for the Taaro GitHub App and coordination API.
type Server struct {
	AppID          int64
	Store          store.Store
	WebhookHandler *webhook.Handler
	Logger         *slog.Logger
}

// Handler returns the configured HTTP router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("POST /webhooks/github", s.WebhookHandler)
	mux.HandleFunc("GET /api/v1/context", s.handleContext)
	mux.HandleFunc("GET /handoffs/{owner}/{repo}/pull/{number}", s.handleHandoffPage)
	return s.logMiddleware(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"app_id": s.AppID,
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rec)
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
	rec, err := s.Store.GetPullRequest(r.Context(), fullName, prNum)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = handoffPageTmpl.Execute(w, rec)
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

var handoffPageTmpl = template.Must(template.New("handoff").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Taaro Handoff — {{.RepoFullName}}#{{.Number}}</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 720px; margin: 2.5rem auto; padding: 0 1rem; line-height: 1.5; }
    code { background: #f4f4f5; padding: 0.15rem 0.35rem; border-radius: 4px; }
  </style>
</head>
<body>
  <h1>{{.RepoFullName}} #{{.Number}}: {{.Title}}</h1>
  <ul>
    <li><strong>State:</strong> {{.State}} (draft: {{.Draft}})</li>
    <li><strong>Head SHA:</strong> <code>{{.HeadSHA}}</code></li>
    <li><strong>Base branch:</strong> <code>{{.BaseRef}}</code></li>
    <li><strong>Last action:</strong> <code>{{.LastAction}}</code></li>
    <li><strong>Observed at:</strong> <code>{{.ObservedAt.Format "2006-01-02T15:04:05Z07:00"}}</code></li>
    <li><strong>GitHub PR:</strong> <a href="{{.HTMLURL}}" target="_blank" rel="noreferrer">{{.HTMLURL}}</a></li>
  </ul>
</body>
</html>`))

package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manifest defines the GitHub App Manifest payload submitted to GitHub during setup.
type Manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     HookAttributes    `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

// HookAttributes configures the webhook endpoint in the manifest.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// BuildManifest builds a GitHub App Manifest matching docs/PROJECT.md section 7.
func BuildManifest(name, baseURL, webhookURL string, includeContentsRead bool) Manifest {
	perms := map[string]string{
		"metadata":      "read",
		"pull_requests": "read",
		"issues":        "read",
		"checks":        "write",
		"merge_queues":  "read",
	}
	if includeContentsRead {
		perms["contents"] = "read"
	}

	return Manifest{
		Name: name,
		URL:  baseURL,
		HookAttributes: HookAttributes{
			URL:    webhookURL,
			Active: true,
		},
		RedirectURL:        strings.TrimRight(baseURL, "/") + "/setup/callback",
		Public:             false,
		DefaultPermissions: perms,
		DefaultEvents: []string{
			"pull_request",
			"pull_request_review",
			"merge_group",
			"issues",
			"check_run",
			"check_suite",
		},
	}
}

// ConversionResponse holds the credentials returned by GitHub's manifest conversion endpoint.
type ConversionResponse struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	HTMLURL       string `json:"html_url"`
}

// SetupServer serves the local one-click GitHub App Manifest registration wizard.
type SetupServer struct {
	BaseURL        string
	EnvPath        string
	PrivateKeyPath string
	GitHubAPIBase  string
	DefaultSmeeURL string
	HTTPClient     *http.Client
	Logger         *slog.Logger

	mu             sync.Mutex
	lastWebhookURL string
	state          string
}

// Handler returns the HTTP handler for the manifest bootstrap flow.
func (s *SetupServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("POST /setup/start", s.handleStart)
	mux.HandleFunc("GET /setup/callback", s.handleCallback)
	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *SetupServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = setupFormTmpl.Execute(w, map[string]any{
		"DefaultName":    "taaro-dev",
		"DefaultSmeeURL": s.DefaultSmeeURL,
		"EnvPath":        s.EnvPath,
		"PrivateKeyPath": s.PrivateKeyPath,
	})
}

func (s *SetupServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	appName := strings.TrimSpace(r.FormValue("app_name"))
	if appName == "" {
		appName = "taaro-dev"
	}
	webhookURL := strings.TrimSpace(r.FormValue("webhook_url"))
	if webhookURL == "" {
		http.Error(w, "webhook_url is required (create a channel at https://smee.io/new)", http.StatusBadRequest)
		return
	}
	org := strings.TrimSpace(r.FormValue("org"))
	includeContents := r.FormValue("include_contents") == "on"

	u, err := url.Parse(webhookURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || strings.ContainsAny(webhookURL, "\r\n") {
		http.Error(w, "enter an HTTPS webhook URL", http.StatusBadRequest)
		return
	}
	state := rand.Text()
	s.mu.Lock()
	s.lastWebhookURL, s.state = webhookURL, state
	s.mu.Unlock()

	manifest := BuildManifest(appName, s.BaseURL, webhookURL, includeContents)
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, "failed to encode manifest", http.StatusInternalServerError)
		return
	}

	actionURL := "https://github.com/settings/apps/new"
	if org != "" {
		actionURL = fmt.Sprintf("https://github.com/organizations/%s/settings/apps/new", org)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = manifestPostTmpl.Execute(w, map[string]any{
		"ActionURL":    actionURL + "?state=" + state,
		"ManifestJSON": string(rawManifest),
	})
}

func (s *SetupServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing ?code query parameter from GitHub", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "" || r.URL.Query().Get("state") != s.state {
		http.Error(w, "restart registration in the setup wizard", http.StatusBadRequest)
		return
	}
	s.state = ""
	conv, err := s.convertManifestCode(r.Context(), code)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("manifest conversion failed", "err", err)
		}
		http.Error(w, fmt.Sprintf("manifest conversion failed: %v", err), http.StatusBadGateway)
		return
	}

	if err := s.writeCredentials(conv); err != nil {
		if s.Logger != nil {
			s.Logger.Error("failed writing credentials", "err", err)
		}
		http.Error(w, fmt.Sprintf("failed saving local credentials: %v", err), http.StatusInternalServerError)
		return
	}

	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new", conv.Slug)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = setupDoneTmpl.Execute(w, map[string]any{
		"AppName":        conv.Name,
		"AppID":          conv.ID,
		"AppSlug":        conv.Slug,
		"InstallURL":     installURL,
		"EnvPath":        s.EnvPath,
		"PrivateKeyPath": s.PrivateKeyPath,
	})
}

func (s *SetupServer) convertManifestCode(ctx context.Context, code string) (ConversionResponse, error) {
	apiBase := strings.TrimRight(s.GitHubAPIBase, "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/app-manifests/%s/conversions", apiBase, code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return ConversionResponse{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return ConversionResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ConversionResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ConversionResponse{}, fmt.Errorf("github returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var conv ConversionResponse
	if err := json.Unmarshal(body, &conv); err != nil {
		return ConversionResponse{}, fmt.Errorf("decode conversion response: %w", err)
	}
	if conv.ID == 0 || conv.PEM == "" {
		return ConversionResponse{}, fmt.Errorf("incomplete conversion response from GitHub")
	}
	return conv, nil
}

func (s *SetupServer) writeCredentials(conv ConversionResponse) error {
	keyPath := s.PrivateKeyPath
	if keyPath == "" {
		keyPath = filepath.Join(".secrets", "taaro-dev.pem")
	}
	if dir := filepath.Dir(keyPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create secret directory %s: %w", dir, err)
		}
	}
	if err := writePrivateFile(keyPath, []byte(conv.PEM)); err != nil {
		return fmt.Errorf("write private key %s: %w", keyPath, err)
	}

	smeeURL := s.lastWebhookURL
	if smeeURL == "" {
		smeeURL = s.DefaultSmeeURL
	}

	envContent := fmt.Sprintf(`# Generated by taaro-server -setup at %s
TAARO_LISTEN_ADDR=127.0.0.1:8080
TAARO_PUBLIC_URL=%s
TAARO_APP_ID=%d
TAARO_APP_SLUG=%s
TAARO_WEBHOOK_SECRET=%s
TAARO_PRIVATE_KEY_PATH=%s
TAARO_CLIENT_ID=%s
TAARO_CLIENT_SECRET=%s
TAARO_SMEE_URL=%s
TAARO_DB_PATH=.data/taaro.db
TAARO_DUMP_WEBHOOKS=
TAARO_LOG_LEVEL=debug
TAARO_SERVER_URL=%s
`,
		time.Now().UTC().Format(time.RFC3339),
		s.BaseURL,
		conv.ID,
		conv.Slug,
		conv.WebhookSecret,
		keyPath,
		conv.ClientID,
		conv.ClientSecret,
		smeeURL,
		s.BaseURL,
	)

	envPath := s.EnvPath
	if envPath == "" {
		envPath = ".env"
	}
	if err := writePrivateFile(envPath, []byte(envContent)); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	return nil
}

// Rename a complete mode-0600 file into place, including when replacing .env.
func writePrivateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".taaro-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return os.Rename(f.Name(), path)
}

var setupFormTmpl = template.Must(template.New("setupForm").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Taaro GitHub App Setup</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 640px; margin: 3rem auto; padding: 0 1rem; line-height: 1.5; }
    label { display: block; margin-top: 1rem; font-weight: 600; }
    input[type="text"], input[type="url"] { width: 100%; padding: 0.5rem; margin-top: 0.25rem; box-sizing: border-box; }
    .hint { color: #555; font-size: 0.9rem; margin-top: 0.2rem; }
    button { margin-top: 1.5rem; padding: 0.65rem 1.25rem; font-size: 1rem; cursor: pointer; }
  </style>
</head>
<body>
  <h1>Create a local Taaro GitHub App</h1>
  <p>This wizard uses GitHub's App Manifest flow to register a private dev app with PR and check permissions. PR access includes descriptions, comments, and diffs. The wizard saves credentials to <code>{{.EnvPath}}</code> and <code>{{.PrivateKeyPath}}</code>.</p>
  <form method="POST" action="/setup/start">
    <label for="app_name">GitHub App name</label>
    <input id="app_name" name="app_name" type="text" value="{{.DefaultName}}" required />
    <div class="hint">Must be globally unique on GitHub (e.g. <code>taaro-dev-yourname</code>).</div>

    <label for="webhook_url">Webhook forwarding URL</label>
    <input id="webhook_url" name="webhook_url" type="url" value="{{.DefaultSmeeURL}}" placeholder="https://smee.io/your-channel-id" required />
    <div class="hint">Create a persistent channel at <a href="https://smee.io/new" target="_blank" rel="noreferrer">https://smee.io/new</a> (or paste your <code>cloudflared</code> URL + <code>/webhooks/github</code>).</div>

    <label for="org">GitHub organization (optional)</label>
    <input id="org" name="org" type="text" placeholder="Leave blank for your personal account" />

    <label>
      <input name="include_contents" type="checkbox" />
      Request optional <code>Contents: read</code> permission (for versioned RFCs)
    </label>

    <button type="submit">Register GitHub App</button>
  </form>
</body>
</html>`))

var manifestPostTmpl = template.Must(template.New("manifestPost").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8" /><title>Redirecting to GitHub...</title></head>
<body>
  <p>Redirecting to GitHub to create your App...</p>
  <form id="manifest-form" method="POST" action="{{.ActionURL}}">
    <input type="hidden" name="manifest" value="{{.ManifestJSON}}" />
    <button type="submit">Continue to GitHub</button>
  </form>
  <script>document.getElementById("manifest-form").submit();</script>
</body>
</html>`))

var setupDoneTmpl = template.Must(template.New("setupDone").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Taaro GitHub App Created</title>
  <style>body { font-family: system-ui, sans-serif; max-width: 640px; margin: 3rem auto; padding: 0 1rem; line-height: 1.5; }</style>
</head>
<body>
  <h1>GitHub App created!</h1>
  <ul>
    <li><strong>Name:</strong> {{.AppName}}</li>
    <li><strong>App ID:</strong> <code>{{.AppID}}</code></li>
    <li><strong>Saved env:</strong> <code>{{.EnvPath}}</code></li>
    <li><strong>Saved private key:</strong> <code>{{.PrivateKeyPath}}</code></li>
  </ul>
  <h2>Next steps</h2>
  <ol>
    <li><a href="{{.InstallURL}}" target="_blank" rel="noreferrer">Install the App on your selected repository</a>.</li>
    <li>Stop this setup server (<code>Ctrl+C</code>).</li>
    <li>Start local development: run <code>make dev</code> in one terminal and <code>make smee</code> in another.</li>
  </ol>
</body>
</html>`))

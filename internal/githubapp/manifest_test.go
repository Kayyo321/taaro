package githubapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildManifestPermissionsAndEvents(t *testing.T) {
	m := BuildManifest("taaro-dev", "http://127.0.0.1:8080", "https://smee.io/abc123", false)
	if m.DefaultPermissions["metadata"] != "read" ||
		m.DefaultPermissions["pull_requests"] != "read" ||
		m.DefaultPermissions["issues"] != "read" ||
		m.DefaultPermissions["checks"] != "write" {
		t.Fatalf("unexpected permissions: %+v", m.DefaultPermissions)
	}
	if slices.Contains(m.DefaultEvents, "merge_group") {
		t.Fatal("merge_group requires a separate permission")
	}
	if _, hasContents := m.DefaultPermissions["contents"]; hasContents {
		t.Fatalf("contents permission should be omitted by default")
	}
	if m.RedirectURL != "http://127.0.0.1:8080/setup/callback" {
		t.Fatalf("unexpected redirect URL: %s", m.RedirectURL)
	}

	withContents := BuildManifest("taaro-dev", "http://127.0.0.1:8080", "https://smee.io/abc123", true)
	if withContents.DefaultPermissions["contents"] != "read" {
		t.Fatalf("expected optional contents:read permission")
	}
}

func TestSetupCallbackSavesCredentials(t *testing.T) {
	fakeGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/test-code/conversions" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(ConversionResponse{
			ID:            12345,
			Slug:          "taaro-dev-test",
			Name:          "Taaro Dev Test",
			ClientID:      "Iv1.abc",
			ClientSecret:  "secret-client",
			WebhookSecret: "whsec-test",
			PEM:           "-----BEGIN RSA PRIVATE KEY-----\nTEST\n-----END RSA PRIVATE KEY-----\n",
		})
	}))
	defer fakeGH.Close()

	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	keyPath := filepath.Join(dir, ".secrets", "taaro-dev.pem")

	srv := &SetupServer{
		BaseURL:        "http://127.0.0.1:8080",
		EnvPath:        envPath,
		PrivateKeyPath: keyPath,
		GitHubAPIBase:  fakeGH.URL,
		DefaultSmeeURL: "https://smee.io/test",
		HTTPClient:     fakeGH.Client(),
	}

	form := strings.NewReader("webhook_url=https%3A%2F%2Fsmee.io%2Ftest")
	start := httptest.NewRequest(http.MethodPost, "/setup/start", form)
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, start)
	if srv.state == "" || !strings.Contains(page.Body.String(), "?state="+srv.state) {
		t.Fatal("registration form missing callback state")
	}
	bad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/setup/callback?code=test-code", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatal("accepted a callback without state")
	}
	if err := os.WriteFile(envPath, []byte("old config"), 0644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/setup/callback?code=test-code&state="+srv.state, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	if !strings.Contains(string(keyBytes), "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("unexpected key content: %s", string(keyBytes))
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	info, err := os.Stat(envPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("expected private environment file, got %v (%v)", info, err)
	}
	envText := string(envBytes)
	for _, want := range []string{"TAARO_APP_ID=12345", "TAARO_WEBHOOK_SECRET=whsec-test", "TAARO_SMEE_URL=https://smee.io/test"} {
		if !strings.Contains(envText, want) {
			t.Fatalf("expected %q in .env, got:\n%s", want, envText)
		}
	}
}

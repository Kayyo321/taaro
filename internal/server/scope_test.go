package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kayyo321/taaro/internal/server"
)

func TestRepositoryScopeRejectsOtherRepositoryBeforeLookup(t *testing.T) {
	srv := (&server.Server{APIToken: "test-token", Repository: "acme/widgets"}).Handler()
	for _, test := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/context?repo=acme/other&pr=1", ""},
		{http.MethodHead, "/handoffs/acme/other/pull/1?repo=acme/widgets", ""},
		{http.MethodPost, "/api/v1/artifacts?repo=acme/widgets", `{"repo":"acme/widgets","Repo":"acme/other"}`},
		{http.MethodPost, "/api/v1/awareness/verify-pr", `{"repo":"acme/other","publish":true}`},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", test.method, test.path, w.Code)
		}
	}
}

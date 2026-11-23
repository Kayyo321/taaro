package adapter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kayyo321/taaro/internal/adapter"
)

func TestClientResponseSize(t *testing.T) {
	t.Run("aggregated response above request limit", func(t *testing.T) {
		payload := strings.Repeat("a", 5<<20)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"artifacts":[{"payload":"`)
			io.WriteString(w, payload)
			io.WriteString(w, `"}]}`)
		}))
		defer srv.Close()

		artifacts, err := adapter.NewClient(srv.URL, "").ListArtifacts(context.Background(), "acme/widgets")
		if err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 1 || artifacts[0].Payload != payload {
			t.Fatal("expected complete artifact list")
		}
	})

	t.Run("document prefix cannot hide oversized response", func(t *testing.T) {
		whitespace := strings.Repeat(" ", 1<<20)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := io.WriteString(w, `{"artifacts":[]}`); err != nil {
				return
			}
			for range 33 {
				if _, err := io.WriteString(w, whitespace); err != nil {
					return
				}
			}
		}))
		defer srv.Close()

		_, err := adapter.NewClient(srv.URL, "").ListArtifacts(context.Background(), "acme/widgets")
		if err == nil || !strings.Contains(err.Error(), "exceeds the 32 MiB client limit") {
			t.Fatalf("expected explicit response limit error, got %v", err)
		}
	})
}

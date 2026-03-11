package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendWebhookJSON(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := sendWebhookJSON(context.Background(), server.Client(), server.URL, []byte(`{"ok":true}`), "build failed", "send failed", "unexpected status: %d")
		if err != nil {
			t.Fatalf("expected success, got %v", err)
		}
	})

	t.Run("build error", func(t *testing.T) {
		err := sendWebhookJSON(context.Background(), &http.Client{}, "://bad-url", []byte(`{}`), "build failed", "send failed", "unexpected status: %d")
		if err == nil || err.Error() != "build failed" {
			t.Fatalf("expected build error, got %v", err)
		}
	})

	t.Run("send error", func(t *testing.T) {
		err := sendWebhookJSON(context.Background(), &http.Client{}, "http://127.0.0.1:1", []byte(`{}`), "build failed", "send failed", "unexpected status: %d")
		if err == nil || err.Error() != "send failed" {
			t.Fatalf("expected send error, got %v", err)
		}
	})

	t.Run("status error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		err := sendWebhookJSON(context.Background(), server.Client(), server.URL, []byte(`{}`), "build failed", "send failed", "unexpected status: %d")
		if err == nil || !strings.Contains(err.Error(), "unexpected status: 502") {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

func TestCloseResponseBodyNilSafe(t *testing.T) {
	closeResponseBody(nil)
	closeResponseBody(&http.Response{})
}

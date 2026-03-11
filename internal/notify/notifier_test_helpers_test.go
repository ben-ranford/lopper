package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func assertNotifyFailures(t *testing.T, n Notifier) {
	t.Helper()

	if n.Notify(context.Background(), Delivery{WebhookURL: "://bad"}) == nil {
		t.Fatalf("expected request build error")
	}

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer statusServer.Close()

	if n.Notify(context.Background(), Delivery{WebhookURL: statusServer.URL}) == nil {
		t.Fatalf("expected non-2xx status error")
	}

	if n.Notify(context.Background(), Delivery{WebhookURL: "http://127.0.0.1:1"}) == nil {
		t.Fatalf("expected send failure for unreachable endpoint")
	}
}

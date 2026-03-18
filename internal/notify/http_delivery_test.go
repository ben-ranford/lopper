package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	buildFailedErrMsg = "build failed"
	sendFailedErrMsg  = "send failed"
	unexpectedStatus  = "unexpected status: %d"
)

func TestSendWebhookJSONSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := sendWebhookJSON(context.Background(), server.Client(), server.URL, []byte(`{"ok":true}`), buildFailedErrMsg, sendFailedErrMsg, unexpectedStatus)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSendWebhookJSONBuildError(t *testing.T) {
	assertWebhookJSONError(t, "://bad-url", buildFailedErrMsg)
}

func TestSendWebhookJSONSendError(t *testing.T) {
	assertWebhookJSONError(t, "http://127.0.0.1:1", sendFailedErrMsg)
}

func TestSendWebhookJSONStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	err := sendWebhookJSON(context.Background(), server.Client(), server.URL, []byte(`{}`), buildFailedErrMsg, sendFailedErrMsg, unexpectedStatus)
	if err == nil || !strings.Contains(err.Error(), "unexpected status: 502") {
		t.Fatalf("expected status error, got %v", err)
	}
}

func TestSendWebhookJSONIgnoresCloseError(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReadCloser{Reader: strings.NewReader(`{"ok":true}`), closeErr: errors.New("close failed")},
				Header:     make(http.Header),
			}, nil
		}),
	}

	err := sendWebhookJSON(context.Background(), client, "https://example.test/webhook", []byte(`{"ok":true}`), buildFailedErrMsg, sendFailedErrMsg, unexpectedStatus)
	if err != nil {
		t.Fatalf("expected success despite close error, got %v", err)
	}
}

func TestCloseResponseBodyNilSafe(t *testing.T) {
	closeResponseBody(nil)
	closeResponseBody(&http.Response{})
}

func assertWebhookJSONError(t *testing.T, endpoint, want string) {
	t.Helper()

	err := sendWebhookJSON(context.Background(), &http.Client{}, endpoint, []byte(`{}`), buildFailedErrMsg, sendFailedErrMsg, unexpectedStatus)
	if err == nil || !strings.Contains(err.Error(), want) || err.Error() == want {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReadCloser struct {
	io.Reader
	closeErr error
}

func (r *errReadCloser) Close() error {
	return r.closeErr
}

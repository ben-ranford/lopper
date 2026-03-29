package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const decodePayloadErrFmt = "decode payload: %v"
const exampleHookURL = "https://example.com/hook"

type payloadCaptureServer struct {
	*httptest.Server
	payload map[string]any
}

func newPayloadCaptureServer(t *testing.T) *payloadCaptureServer {
	t.Helper()

	capture := &payloadCaptureServer{}
	capture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		defer func() {
			if err := r.Body.Close(); err != nil {
				t.Fatalf("close request body: %v", err)
			}
		}()

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf(decodePayloadErrFmt, err)
		}

		capture.payload = payload
		w.WriteHeader(http.StatusOK)
	}))

	return capture
}

package thresholds

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type staticReadCloser struct {
	data     []byte
	offset   int
	closeErr error
}

func (r *staticReadCloser) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *staticReadCloser) Close() error {
	return r.closeErr
}

type errReadCloser struct {
	readErr  error
	closeErr error
}

func (r *errReadCloser) Read([]byte) (int, error) {
	return 0, r.readErr
}

func (r *errReadCloser) Close() error {
	return r.closeErr
}

func TestThresholdConfigAdditionalRemotePolicyBranches(t *testing.T) {
	t.Run("resolver surfaces canonical location failures", func(t *testing.T) {
		if _, err := newPackResolver(t.TempDir()).resolveFile("https://example.com/policy.yml#bad-pin", packTrust{explicit: true}); err == nil {
			t.Fatalf("expected resolveFile to reject invalid canonical policy locations")
		}
	})

	t.Run("resolve remote pack ref against parent URL", func(t *testing.T) {
		current := "https://example.com/policies/root.yml#sha256=" + strings.Repeat("a", 64)
		got, err := resolvePackRef(current, "../shared/base.yml#sha256="+strings.Repeat("b", 64))
		if err != nil {
			t.Fatalf("resolve remote relative pack ref: %v", err)
		}
		want := "https://example.com/shared/base.yml#sha256=" + strings.Repeat("b", 64)
		if got != want {
			t.Fatalf("unexpected resolved remote pack ref: got %q want %q", got, want)
		}
	})

	t.Run("read remote policy joins close errors", func(t *testing.T) {
		body := []byte("thresholds:\n  fail_on_increase_percent: 1\n")
		sum := sha256.Sum256(body)
		closeErr := errors.New("close body")

		originalClient := remotePolicyHTTPClient
		t.Cleanup(func() {
			remotePolicyHTTPClient = originalClient
		})
		remotePolicyHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &staticReadCloser{data: body, closeErr: closeErr},
			}, nil
		})}

		location := "https://example.com/policy.yml#sha256=" + hex.EncodeToString(sum[:])
		if _, err := readRemotePolicyFile(location); err == nil || !strings.Contains(err.Error(), closeErr.Error()) {
			t.Fatalf("expected joined remote policy close error, got %v", err)
		}
	})

	t.Run("read remote policy surfaces body read errors", func(t *testing.T) {
		readErr := errors.New("read body")

		originalClient := remotePolicyHTTPClient
		t.Cleanup(func() {
			remotePolicyHTTPClient = originalClient
		})
		remotePolicyHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReadCloser{readErr: readErr},
			}, nil
		})}

		location := "https://example.com/policy.yml#sha256=" + strings.Repeat("a", 64)
		if _, err := readRemotePolicyFile(location); err == nil || !strings.Contains(err.Error(), "read remote policy response") {
			t.Fatalf("expected remote policy read error, got %v", err)
		}
	})
}

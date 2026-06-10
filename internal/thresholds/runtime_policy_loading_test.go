package thresholds

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThresholdConfigAdditionalRuntimeBranches(t *testing.T) {
	if _, err := resolvePackRef("https://example.com/policy.yml", "%zz"); err == nil || !strings.Contains(err.Error(), "invalid remote pack reference") {
		t.Fatalf("expected invalid remote pack reference error, got %v", err)
	}
}

func TestReadRemotePolicyFileRejectsOversizedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(strings.Repeat("x", maxRemotePolicyBytes+1))); err != nil {
			t.Fatalf("write oversized response: %v", err)
		}
	}))
	defer server.Close()

	location := server.URL + "/policy.yml#sha256=" + strings.Repeat("a", 64)
	if _, err := readRemotePolicyFile(location); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected oversized remote policy error, got %v", err)
	}
}

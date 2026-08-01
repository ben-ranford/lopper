package advisory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadJSONObjectNameRejectsNonStringToken(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`123`))
	if _, err := readJSONObjectName(decoder); err == nil || !strings.Contains(err.Error(), "field name") {
		t.Fatalf("expected object field name error, got %v", err)
	}
}

func TestDiscardJSONValueAcceptsScalarToken(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`123`))
	if err := discardJSONValue(decoder); err != nil {
		t.Fatalf("discard scalar token: %v", err)
	}
}

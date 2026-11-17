package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func readyFixture(t *testing.T) []byte {
	t.Helper()
	value := probeReady{Kind: "READY", Nonce: "nonce", Executable: "/private/python.exe", Prefix: "/private", Version: []int{3, 13, 16}, Origins: map[string]*string{"json": nil, "re": nil, "encodings": nil, "_json": nil, "_sre": nil}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestProbeRejectsMissingExtraAndWrongNonce(t *testing.T) {
	data := readyFixture(t)
	if _, err := decodeReady(data, "nonce", "/private", "/private/python.exe"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, data[:len(data)-1], append(append([]byte{}, data...), data...), []byte(strings.Replace(string(data), "3,13,16", "3,13,15", 1))} {
		if _, err := decodeReady(bad, "nonce", "/private", "/private/python.exe"); err == nil {
			t.Fatal("invalid probe frame accepted")
		}
	}
}

func TestProbeOutputOverflowCancelsBeforeRetention(t *testing.T) {
	cancelled := false
	output := &limitedOutput{limit: 3, cancel: func() { cancelled = true }}
	if n, err := output.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("exact output rejected: %d %v", n, err)
	}
	if n, err := output.Write([]byte("d")); err == nil || n != 0 || !cancelled {
		t.Fatalf("overflow did not cancel: %d %v %v", n, err, cancelled)
	}
	if string(output.bytes()) != "abc" {
		t.Fatal("overflow retained payload")
	}
}

func TestProbeJSONRejectsStructuralAmbiguityAtItsBoundary(t *testing.T) {
	for name, fixture := range map[string]struct{ input, category string }{
		"duplicate": {`{"nonce":"a","nonce":"b"}`, "duplicate"},
		"trailing":  {`{} {}`, "trailing"},
		"depth":     {strings.Repeat("[", maxReceiptDepth+1) + "0" + strings.Repeat("]", maxReceiptDepth+1), "nesting"},
		"count":     {"[" + strings.Repeat("0,", maxFields) + "0]", "field limit"},
		"token":     {`"` + strings.Repeat("a", maxStringBytes+1) + `"`, "token"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateProbeJSON([]byte(fixture.input))
			if err == nil || !strings.Contains(err.Error(), fixture.category) {
				t.Fatalf("wanted %s rejection, got %v", fixture.category, err)
			}
		})
	}
	for _, input := range []string{"", "[", `{"a":`, `{"a":1`, `{"a":1,`, "]"} {
		if err := validateProbeJSON([]byte(input)); err == nil {
			t.Fatalf("malformed JSON accepted: %q", input)
		}
	}
}

func TestReadyRejectsUnknownFieldsAndIncompleteOrigins(t *testing.T) {
	good := string(readyFixture(t))
	for _, bad := range []string{
		strings.Replace(good, `"kind":"READY"`, `"unknown":true,"kind":"READY"`, 1),
		strings.Replace(good, `"_sre":null,`, "", 1),
		strings.Replace(good, `"_sre"`, `"other"`, 1),
		"[]\n", "{\n", strings.Repeat("x", maxHandshakeBytes+1) + "\n",
	} {
		if _, err := decodeReady([]byte(bad), "nonce", "/private", "/private/python.exe"); err == nil {
			t.Fatal("invalid READY identity accepted")
		}
	}
}

func TestReadyRejectsValidSchemaWithWrongIdentity(t *testing.T) {
	data := readyFixture(t)
	for name, identity := range map[string]struct{ nonce, root, executable string }{
		"nonce":      {"other", "/private", "/private/python.exe"},
		"prefix":     {"nonce", "/other", "/private/python.exe"},
		"executable": {"nonce", "/private", "/private/other.exe"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeReady(data, identity.nonce, identity.root, identity.executable)
			if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
				t.Fatalf("expected identity mismatch, got %v", err)
			}
		})
	}
}

package baseline

import (
	"strings"
	"testing"
)

func TestDecodeSnapshotSkipsLargeReportPayload(t *testing.T) {
	t.Parallel()

	data := []byte(`{"baselineSchemaVersion":"1.0.0","report":{"payload":"` + strings.Repeat("x", 1<<20) + `"}}`)
	_, _, err := DecodeSnapshot(data, normalizedSnapshotDecodeOptions())
	if err != nil {
		t.Fatalf("DecodeSnapshot() error = %v", err)
	}
}

func TestDecodeSnapshotRejectsTypedSyntaxAfterRecognition(t *testing.T) {
	t.Parallel()

	for _, data := range []string{
		`{"report":"unterminated}`,
		`{"baselineSchemaVersion":"1.0.0"`,
		`{"report":{},"\\q":true}`,
		`{"baselineSchemaVersion":"1.0.0" "report":{"value":"snapshot"}}`,
		`{"baselineSchemaVersion":"1.0.0","report":{"value":"snapshot"}} trailing`,
	} {
		if _, _, err := DecodeSnapshot([]byte(data), normalizedSnapshotDecodeOptions()); err == nil {
			t.Fatalf("DecodeSnapshot(%q) returned nil error", data)
		}
	}
}

func TestDecodeSnapshotExercisesEnvelopeValueScanning(t *testing.T) {
	t.Parallel()

	for _, data := range []string{
		`[]`,
		`{"`,
		`{"report"}`,
		`{"baselineSchemaVersion":`,
		`{"baselineSchemaVersion":{`,
		`{"baselineSchemaVersion":true}`,
		`{"baselineSchemaVersion":"\q"}`,
		`{"\q":true}`,
		`{"baselineSchemaVersion":"value\`,
		"{\"baselineSchemaVersion\":\"value\n\"}",
		`{}`,
		`{"report":`,
		`{"report":true`,
		`{"report":"value\"quoted","baselineSchemaVersion":"1.0.0"}`,
		`{"report":{"items":[1,"two",{"nested":true}]},"baselineSchemaVersion":"1.0.0"}`,
		`{"report":1e1000,"baselineSchemaVersion":"1.0.0"}`,
		`{"report":true,"baselineSchemaVersion":"1.0.0"}`,
		`{"report":null,"baselineSchemaVersion":"1.0.0"}`,
		`{"report": ["value"], "baselineSchemaVersion":"1.0.0"}`,
		`{"report":"value\\","baselineSchemaVersion":"1.0.0"}`,
		"{\"report\":\"value\n\",\"baselineSchemaVersion\":\"1.0.0\"}",
		`{"report":{},"\q":true}`,
	} {
		if _, _, err := DecodeSnapshot([]byte(data), normalizedSnapshotDecodeOptions()); err != nil {
			continue
		}
	}
}

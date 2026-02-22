package report

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

func TestFormatSARIFValidatesAgainstSchema(t *testing.T) {
	formatted, err := NewFormatter().Format(sampleSARIFReport(), FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif: %v", err)
	}

	schemaPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "report", "sarif-2.1.0.schema.json"))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}

	result, err := gojsonschema.Validate(
		gojsonschema.NewReferenceLoader("file://"+schemaPath),
		gojsonschema.NewStringLoader(formatted),
	)
	if err != nil {
		t.Fatalf("validate sarif schema: %v", err)
	}
	if result.Valid() {
		return
	}

	messages := make([]string, 0, len(result.Errors()))
	for _, item := range result.Errors() {
		messages = append(messages, item.String())
	}
	t.Fatalf("sarif output failed schema validation: %s", strings.Join(messages, "; "))
}

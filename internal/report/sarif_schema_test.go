package report

import (
	"net/url"
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

	result, err := gojsonschema.Validate(gojsonschema.NewReferenceLoader(fileURLFromPath(schemaPath)), gojsonschema.NewStringLoader(formatted))
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

func fileURLFromPath(path string) string {
	slashed := strings.ReplaceAll(path, "\\", "/")
	slashed = filepath.ToSlash(slashed)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{
		Scheme: "file",
		Path:   slashed,
	}).String()
}

func TestFileURLFromPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unix absolute path",
			in:   "/tmp/sarif schema.json",
			want: "file:///tmp/sarif%20schema.json",
		},
		{
			name: "windows absolute path",
			in:   "C:\\tmp\\sarif schema.json",
			want: "file:///C:/tmp/sarif%20schema.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileURLFromPath(tc.in); got != tc.want {
				t.Fatalf("fileURLFromPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

package elixir

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseImportsFromSanitizedSeparatesSourceSanitizer(t *testing.T) {
	content := []byte("defmodule Demo do\n  message = \"\"\"\n  alias Foo.Bar\n  \"\"\"\n  alias Foo.Bar, as: Baz\nend\n")
	sanitized := sanitizeElixirSource(content)

	realAlias := []byte("alias Foo.Bar, as: Baz")
	index := bytes.Index(content, realAlias)
	if index < 0 {
		t.Fatalf("expected fixture to contain non-heredoc alias text")
	}
	for i := index; i < index+len(realAlias); i++ {
		sanitized[i] = ' '
	}

	imports := parseImportsFromSanitized(content, sanitized, "lib/demo.ex", map[string]struct{}{"foo": {}})
	if len(imports) != 0 {
		t.Fatalf("expected no imports after masking non-heredoc alias in pre-sanitized source, got %#v", imports)
	}
}

func TestParseImportsFromSanitizedRejectsMismatchedBufferLengths(t *testing.T) {
	content := []byte("defmodule Demo do\n  alias Foo.Bar\nend\n")
	sanitized := sanitizeElixirSource(content)

	imports := parseImportsFromSanitized(content, sanitized[:len(sanitized)-1], "lib/demo.ex", map[string]struct{}{"foo": {}})
	if len(imports) != 0 {
		t.Fatalf("expected mismatched sanitized buffer to produce no imports, got %#v", imports)
	}
}

func TestParseImportsMatchesExplicitSanitizerPipeline(t *testing.T) {
	content := []byte("defmodule Demo do\n  message = \"\"\"\n  alias Foo.Bar\n  \"\"\"\n  alias Foo.Bar, as: Baz\n  import Foo.Bar\nend\n")
	declared := map[string]struct{}{"foo": {}}
	filePath := "lib/demo.ex"

	got := parseImports(content, filePath, declared)
	want := parseImportsFromSanitized(content, sanitizeElixirSource(content), filePath, declared)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected wrapper parse path to match explicit sanitizer pipeline:\nwant %#v\ngot  %#v", want, got)
	}
}

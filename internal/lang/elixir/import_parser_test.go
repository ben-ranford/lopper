package elixir

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseImportsFromSanitizedSeparatesSourceSanitizer(t *testing.T) {
	content := []byte("defmodule Demo do\n  message = \"\"\"\n  alias Foo.Bar\n  \"\"\"\n  alias Foo.Bar, as: Baz\nend\n")
	sanitized := append([]byte(nil), content...)

	heredocAlias := []byte("alias Foo.Bar")
	index := bytes.Index(content, heredocAlias)
	if index < 0 {
		t.Fatalf("expected fixture to contain heredoc alias text")
	}
	for i := index; i < index+len(heredocAlias); i++ {
		sanitized[i] = ' '
	}

	imports := parseImportsFromSanitized(content, sanitized, "lib/demo.ex", map[string]struct{}{"foo": {}})
	if len(imports) != 1 {
		t.Fatalf("expected one import from pre-sanitized source, got %#v", imports)
	}
	if imports[0].Local != "Baz" {
		t.Fatalf("expected alias local Baz, got %q", imports[0].Local)
	}
	if imports[0].Location.Line != 5 {
		t.Fatalf("expected import location line 5, got %d", imports[0].Location.Line)
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

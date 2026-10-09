package ruby

import "testing"

func TestGemfileWhitespaceNamesDoNotPublishDependencies(t *testing.T) {
	out := make(map[string]struct{})
	sources := make(map[string]rubyDependencySource)
	declarations := parseGemfileDeclarations([]byte("gem '   ', path: './vendor'\ngem \"\t\", git: 'https://example.com/gem'\ngem 'rack'\n"))
	applyBundlerDeclarations(out, sources, declarations)
	if len(declarations) != 1 || declarations[0].dependency != "rack" {
		t.Fatalf("unexpected declarations: %#v", declarations)
	}
	if _, ok := out["rack"]; !ok || len(out) != 1 {
		t.Fatalf("unexpected dependency names: %#v", out)
	}
	if info, ok := sources["rack"]; !ok || len(sources) != 1 || !info.Rubygems || !info.DeclaredGemfile || info.Path || info.Git {
		t.Fatalf("blank name contaminated source attribution: %#v", sources)
	}
}

package ruby

import (
	"slices"
	"testing"
)

func TestRubyParseGemfileDeclarationsSeam(t *testing.T) {
	content := []byte(`source 'https://rubygems.org'
gem 'rack'
gem 'private_gem', git: 'https://example.test/private_gem.git'
gem 'local_gem', :path => 'vendor/local_gem'
gem ''
`)

	declarations := parseGemfileDeclarations(content)
	got := declarationTuples(declarations)
	want := []string{
		"rack|rubygems|Gemfile",
		"private-gem|git|Gemfile",
		"local-gem|path|Gemfile",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseGemfileDeclarations()=%#v want %#v", got, want)
	}
}

func TestRubyParseGemfileLockDeclarationsSeam(t *testing.T) {
	content := []byte(`GIT
  specs:
    private_gem (1.0.0)
      rack (>= 2.0)

GEM
  specs:
    httparty (0.22.0)
`)

	got := parseGemfileLockDeclarations(content)
	want := []string{"private-gem", "rack", "httparty"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseGemfileLockDeclarations()=%#v want %#v", got, want)
	}
}

func TestRubyApplyGemfileLockDeclarationsSeam(t *testing.T) {
	content := []byte(`GEM
  specs:
    httparty (0.22.0)
      json (>= 2.0)
    rack (3.1.0)
`)

	out := map[string]struct{}{}
	applyGemfileLockDeclarations(content, out)
	got := sortedMapKeys(out)
	want := []string{"httparty", "json", "rack"}
	if !slices.Equal(got, want) {
		t.Fatalf("applyGemfileLockDeclarations()=%#v want %#v", got, want)
	}
}

func TestRubyParseGemfileLockSourceDeclarationsSeam(t *testing.T) {
	content := []byte(`GIT
  specs:
    private_gem (1.0.0)
      rack (>= 2.0)

PATH
  specs:
    local_gem (0.1.0)

UNKNOWN
  specs:
    ignored_gem (0.1.0)

GEM
  specs:
    httparty (0.22.0)
`)

	declarations := parseGemfileLockSourceDeclarations(content)
	got := declarationTuples(declarations)
	want := []string{
		"private-gem|git|Gemfile.lock",
		"local-gem|path|Gemfile.lock",
		"httparty|rubygems|Gemfile.lock",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseGemfileLockSourceDeclarations()=%#v want %#v", got, want)
	}
}

func TestRubyApplyBundlerDeclarationsSeam(t *testing.T) {
	out := map[string]struct{}{}
	sources := map[string]rubyDependencySource{}
	applyBundlerDeclarations(out, sources, []bundlerDeclaration{{
		dependency: "rack",
		kind:       rubyDependencySourceRubygems,
		signal:     gemfileName,
	}, {
		dependency: "rack",
		kind:       rubyDependencySourceRubygems,
		signal:     gemfileLockName,
	}, {
		dependency: "private-gem",
		kind:       rubyDependencySourceGit,
		signal:     gemfileName,
	}})

	if _, ok := out["rack"]; !ok {
		t.Fatalf("expected rack dependency in output set, got %#v", out)
	}
	if _, ok := out["private-gem"]; !ok {
		t.Fatalf("expected private-gem dependency in output set, got %#v", out)
	}
	rackInfo := sources["rack"]
	if !rackInfo.Rubygems || !rackInfo.DeclaredGemfile || !rackInfo.DeclaredLock {
		t.Fatalf("unexpected rack source attribution: %#v", rackInfo)
	}
	privateInfo := sources["private-gem"]
	if !privateInfo.Git || !privateInfo.DeclaredGemfile || privateInfo.DeclaredLock {
		t.Fatalf("unexpected private-gem source attribution: %#v", privateInfo)
	}
}

func declarationTuples(declarations []bundlerDeclaration) []string {
	out := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		out = append(out, declaration.dependency+"|"+declaration.kind+"|"+declaration.signal)
	}
	return out
}

func sortedMapKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

//go:build !regressionproof

package cpp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCPPRegressionproofExcludedIncludeClassificationBranches(t *testing.T) {
	catalog := newDependencyCatalog()
	catalog.add("parallel-extra", "vcpkg manifest")
	if got := declaredIncludeDependency("parallel/base.h", catalog); got != "parallel-extra" {
		t.Fatalf("expected declared prefix correlation, got %q", got)
	}
	if got := declaredIncludeDependency("", catalog); got != "" {
		t.Fatalf("expected blank include to have no declared dependency, got %q", got)
	}
	if got := declaredIncludeDependency("debug/map", newDependencyCatalog()); got != "" {
		t.Fatalf("expected empty catalog to have no declared dependency, got %q", got)
	}
	if got := declaredIncludeDependency("debug/map", catalog); got != "" {
		t.Fatalf("expected unrelated catalog to have no declared dependency, got %q", got)
	}

	if got := filterIncludeSearchPathsForDelimiter([]includeSearchPath{{Path: "/repo/include"}}, '#'); len(got) != 0 {
		t.Fatalf("expected invalid delimiter to yield nil search paths, got %#v", got)
	}
	if shouldSuppressQualifiedStdHeader("debug/map", includeResolution{Resolved: true, ProvenanceKnown: true, Path: "/tmp/debug/map"}) {
		t.Fatalf("expected non-system known provenance to avoid suppression")
	}
	if !shouldSuppressQualifiedStdHeader("regex", includeResolution{Resolved: true, ProvenanceKnown: true, System: true, Path: "/usr/local/include/regex"}) {
		t.Fatalf("expected leaf standard header to be suppressed")
	}
	if !isLikelyMultiarchIncludePrefix("x86_64-linux-android") {
		t.Fatalf("expected android multiarch prefix to be recognized")
	}
	if isLikelyMultiarchIncludePrefix("x86_64-linux-unknownabi") {
		t.Fatalf("expected unknown linux multiarch ABI to be rejected")
	}
	if isLikelyMultiarchIncludePrefix("x86_64-darwin-gnu") {
		t.Fatalf("expected non-linux multiarch prefix to be rejected")
	}
	if isKnownOSCompilerQualifiedHeader("sys") {
		t.Fatalf("expected one-component OS header candidate to be rejected")
	}
	if isKnownOSCompilerQualifiedHeader("sys/") {
		t.Fatalf("expected empty OS header leaf to be rejected")
	}
	if isCompilerDefaultSystemIncludeRoot("") {
		t.Fatalf("expected blank include root not to be compiler default")
	}
	if isLikelySystemIncludePath("") {
		t.Fatalf("expected blank include path not to be system")
	}
	if !isLikelySystemIncludePath("/usr/local/include/sys/types.h") {
		t.Fatalf("expected /usr/local/include header to be system")
	}
	if isLikelySystemIncludePath("/USR/LOCAL/INCLUDE/sys/types.h") {
		t.Fatalf("expected uppercase usr-local include path to remain user-provided")
	}
	if isLikelySystemIncludePath("C:/Build/vendor/include/debug/map") {
		t.Fatalf("expected Windows-style vendor include path not to be system")
	}
	if !isLikelySystemIncludePath("C:/Build/MSVC/include/debug/map") {
		t.Fatalf("expected Windows-style MSVC include path to remain system")
	}
}

func TestCPPRegressionproofExcludedCompileIncludeMergeBranches(t *testing.T) {
	includeDirSet := map[string]struct{}{}
	searchPathSet := map[string]includeSearchPath{
		"/repo/sdk":   {Path: "/repo/sdk"},
		"/repo/quote": {Path: "/repo/quote", QuoteOnly: true, ProvenanceKnown: true},
	}
	recordCompileIncludes(includeDirSet, searchPathSet, []includeSearchPath{
		{},
		{Path: "/repo/sdk", System: true, ProvenanceKnown: true},
		{Path: "/repo/quote", ProvenanceKnown: true},
	})
	if _, ok := includeDirSet["/repo/sdk"]; !ok {
		t.Fatalf("expected include dir set to record nonblank path")
	}
	if !searchPathSet["/repo/sdk"].System {
		t.Fatalf("expected duplicate -isystem path to promote system provenance")
	}
	if searchPathSet["/repo/quote"].QuoteOnly {
		t.Fatalf("expected ordinary duplicate path to replace quote-only provenance")
	}
}

func TestCPPRegressionproofExcludedScanProcessEscapeWarning(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.cpp")
	testutil.MustWriteFile(t, outside, fmtCoreIncludeLine)

	stage := scanStage{scanner: includeResolver{repoPath: repo}}
	if err := stage.process(context.Background(), scanInput{Path: outside}); err != nil {
		t.Fatalf("expected escaped-path scan errors to be downgraded into warnings, got %v", err)
	}
	if len(stage.result.Warnings) != 1 {
		t.Fatalf("expected one escaped-path warning, got %#v", stage.result.Warnings)
	}
}

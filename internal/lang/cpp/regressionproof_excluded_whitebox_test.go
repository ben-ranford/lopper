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

	assertDeclaredIncludeDependency(t, "parallel/base.h", catalog, "parallel-extra")
	assertDeclaredIncludeDependency(t, "", catalog, "")
	assertDeclaredIncludeDependency(t, "debug/map", newDependencyCatalog(), "")
	assertDeclaredIncludeDependency(t, "debug/map", catalog, "")
	assertEmptySearchPaths(t, filterIncludeSearchPathsForDelimiter([]includeSearchPath{{Path: "/repo/include"}}, '#'))
	assertRegressionproofExcludedBooleans(t, []regressionproofExcludedBooleanCase{
		{
			name: "non-system known provenance avoids suppression",
			got:  shouldSuppressQualifiedStdHeader("debug/map", includeResolution{Resolved: true, ProvenanceKnown: true, Path: "/tmp/debug/map"}),
			want: false,
		},
		{
			name: "leaf standard header is suppressed",
			got:  shouldSuppressQualifiedStdHeader("regex", includeResolution{Resolved: true, ProvenanceKnown: true, System: true, Path: "/usr/local/include/regex"}),
			want: true,
		},
		{name: "android multiarch prefix is recognized", got: isLikelyMultiarchIncludePrefix("x86_64-linux-android"), want: true},
		{name: "unknown linux multiarch ABI is rejected", got: isLikelyMultiarchIncludePrefix("x86_64-linux-unknownabi"), want: false},
		{name: "non-linux multiarch prefix is rejected", got: isLikelyMultiarchIncludePrefix("x86_64-darwin-gnu"), want: false},
		{name: "one-component OS header candidate is rejected", got: isKnownOSCompilerQualifiedHeader("sys"), want: false},
		{name: "empty OS header leaf is rejected", got: isKnownOSCompilerQualifiedHeader("sys/"), want: false},
		{name: "blank include root is not compiler default", got: isCompilerDefaultSystemIncludeRoot(""), want: false},
		{name: "blank include path is not system", got: isLikelySystemIncludePath(""), want: false},
		{name: "/usr/local/include header is system", got: isLikelySystemIncludePath("/usr/local/include/sys/types.h"), want: true},
		{name: "uppercase usr-local include path remains user-provided", got: isLikelySystemIncludePath("/USR/LOCAL/INCLUDE/sys/types.h"), want: false},
		{name: "Windows-style vendor include path is not system", got: isLikelySystemIncludePath("C:/Build/vendor/include/debug/map"), want: false},
		{name: "Windows-style MSVC include path is system", got: isLikelySystemIncludePath("C:/Build/MSVC/include/debug/map"), want: true},
	})
}

type regressionproofExcludedBooleanCase struct {
	name string
	got  bool
	want bool
}

func assertDeclaredIncludeDependency(t *testing.T, header string, catalog dependencyCatalog, want string) {
	t.Helper()
	if got := declaredIncludeDependency(header, catalog); got != want {
		t.Fatalf("%s declared dependency: got %q, want %q", header, got, want)
	}
}

func assertEmptySearchPaths(t *testing.T, paths []includeSearchPath) {
	t.Helper()
	if len(paths) != 0 {
		t.Fatalf("expected invalid delimiter to yield nil search paths, got %#v", paths)
	}
}

func assertRegressionproofExcludedBooleans(t *testing.T, cases []regressionproofExcludedBooleanCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s: got %t, want %t", tc.name, tc.got, tc.want)
			}
		})
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

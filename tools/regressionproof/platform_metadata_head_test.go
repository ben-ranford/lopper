package main

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestExplicitRegressionPlatformMatchesSource(t *testing.T) {
	for _, tc := range []struct{ name, file, source, target, want string }{
		{"Darwin filename", "buggy_darwin_test.go", "", "", "darwin"},
		{"Darwin arm64", "buggy_darwin_arm64_test.go", "", "", "darwin"},
		{"Darwin amd64 unsupported", "buggy_darwin_amd64_test.go", "", "", ""},
		{"Darwin tag", "buggy_test.go", "//go:build darwin && arm64\n\n", "", "darwin"},
		{"portable explicit Darwin", "buggy_test.go", "", "darwin", "darwin"},
		{"conflicting Linux metadata", "buggy_darwin_test.go", "", "linux", ""},
		{"conflicting Darwin metadata", "buggy_linux_test.go", "", "darwin", ""},
		{"portable default", "buggy_test.go", "", "", "linux"},
		{"Windows suffix", "buggy_windows_test.go", "", "", "windows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, map[string]string{"buggy/" + tc.file: tc.source + "package buggy\nfunc TestBehavior() {}\n"})
			declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestBehavior", TargetOS: tc.target}
			assertExplicitPlatformRouting(t, repo, declaration, tc.want)
		})
	}
}

func assertExplicitPlatformRouting(t *testing.T, repo string, declaration prmetadata.RegressionDeclaration, want string) {
	t.Helper()
	for _, partition := range []string{"linux", "windows", "darwin"} {
		selected, err := selectTargetDeclarations(repo, []prmetadata.RegressionDeclaration{declaration}, partition)
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "cannot run on a supported native") {
				t.Fatalf("expected unsupported combination: %v", err)
			}
			continue
		}
		if err != nil || (len(selected) == 1) != (partition == want) {
			t.Fatalf("partition %s selected=%v error=%v", partition, selected, err)
		}
	}
}

func TestExplicitLocalPlatformValidation(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{"buggy/buggy_test.go": "package buggy\nfunc TestBehavior() {}\n"})
	for _, platform := range []string{"linux", "windows", "darwin"} {
		declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestBehavior", TargetOS: platform}
		selected, err := selectTargetDeclarations(repo, []prmetadata.RegressionDeclaration{declaration}, "")
		nativeErr := requireNativeProofTarget(platform)
		if (err != nil) != (nativeErr != nil) {
			t.Fatalf("platform %s local error=%v native=%v", platform, err, nativeErr)
		}
		if err == nil && len(selected) != 1 {
			t.Fatal("local proof omitted explicit declaration")
		}
	}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./missing", TestName: "TestBehavior", TargetOS: "darwin"}
	if _, err := selectTargetDeclarations(repo, []prmetadata.RegressionDeclaration{declaration}, ""); err == nil {
		t.Fatal("missing explicit source accepted")
	}
}

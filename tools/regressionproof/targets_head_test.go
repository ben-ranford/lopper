package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestTargetDeclarationRouting(t *testing.T) {
	t.Parallel()
	const testSource = "package buggy\nimport \"testing\"\nfunc TestRegressionProof(t *testing.T) {}\n"
	tests := []struct {
		name, filename, source, target, wantErr string
	}{
		{name: "portable", filename: "buggy_test.go", source: testSource, target: "linux"},
		{name: "Windows suffix", filename: "buggy_windows_test.go", source: testSource, target: "windows"},
		{name: "Windows architecture", filename: "buggy_windows_amd64_test.go", source: testSource, target: "windows"},
		{name: "different architecture", filename: "buggy_windows_arm64_test.go", source: testSource, wantErr: "cannot run on a supported native"},
		{name: "Windows tag", filename: "buggy_test.go", source: "//go:build windows\n\n" + testSource, target: "windows"},
		{name: "mixed OS and cgo", filename: "buggy_test.go", source: "//go:build (linux && !cgo) || (windows && cgo)\n\n" + testSource, target: "windows"},
		{name: "proof tag", filename: "buggy_test.go", source: "//go:build regressionproof\n\n" + testSource, target: "linux"},
		{name: "unsupported OS", filename: "buggy_freebsd_test.go", source: testSource, wantErr: "cannot run on a supported native"},
		{name: "unsupported tag", filename: "buggy_test.go", source: "//go:build customtag\n\n" + testSource, wantErr: "cannot run on a supported native"},
		{name: "invalid tag", filename: "buggy_test.go", source: "//go:build windows &&\n\n" + testSource, wantErr: "parsing //go:build line"},
		{name: "missing test", filename: "buggy_test.go", source: "package buggy\n", wantErr: "no matching test function"},
		{name: "method is not a test", filename: "buggy_test.go", source: "package buggy\ntype T struct{}\nfunc (T) TestRegressionProof() {}\n", wantErr: "no matching test function"},
		{name: "invalid Go", filename: "buggy_test.go", source: "package buggy\nfunc", wantErr: "parse regression source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, map[string]string{"buggy/" + tt.filename: tt.source, "buggy/readme.txt": "fixture"})
			assertTargetDeclarationRouting(t, repo, tt.target, tt.wantErr)
		})
	}
}

func assertTargetDeclarationRouting(t *testing.T, repo, wantTarget, wantErr string) {
	t.Helper()
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	for _, target := range []string{"linux", "windows", "darwin"} {
		selected, err := selectTargetDeclarations(repo, []prmetadata.RegressionDeclaration{declaration}, target)
		if wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("target %s error = %v, want %q", target, err, wantErr)
			}
			continue
		}
		if err != nil || (len(selected) == 1) != (target == wantTarget) {
			t.Fatalf("target %s selection = %v, %v; want only %s", target, selected, err, wantTarget)
		}
	}
}

func TestDeclarationTargetRejectsAmbiguityAndEscapes(t *testing.T) {
	t.Parallel()
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	repo := t.TempDir()
	if _, err := declarationTarget(repo, declaration); err == nil {
		t.Fatal("missing package accepted")
	}
	source := "package buggy\nfunc TestRegressionProof() {}\n"
	writeFiles(t, repo, map[string]string{"buggy/one_test.go": source, "buggy/two_windows_test.go": source})
	if _, err := declarationTarget(repo, declaration); err == nil || !strings.Contains(err.Error(), "multiple files") {
		t.Fatalf("ambiguous declaration accepted: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside_test.go")
	if err := os.WriteFile(outside, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, "buggy", "escape_test.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := declarationTarget(repo, declaration); err == nil || !strings.Contains(err.Error(), "read regression source") {
		t.Fatalf("escaping test source accepted: %v", err)
	}
}

func TestProofTargetValidation(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"", "linux", "windows", "darwin"} {
		if err := validateProofTarget(target); err != nil {
			t.Fatal(err)
		}
		if err := requireNativeProofTarget(target); (err != nil) != (target != "" && (target != runtime.GOOS || runtime.GOARCH != proofTargetArchitecture(target))) {
			t.Fatalf("native target %q returned %v on %s", target, err, runtime.GOOS)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--target-os", "freebsd"}, func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("unsupported target returned %d: %s", code, &stderr)
	}
}

func TestRunRejectsInvalidTargetProofs(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{
		"buggy/buggy_windows_test.go": "package buggy\nfunc TestRegressionProof() {}\n",
	})
	env := regressionProofEnv(map[string]string{
		"PR_TITLE": "fix(ci): route native proof", "PR_BASE_SHA": "base", "PR_BODY": regressionProofBody(),
	})
	var stderr bytes.Buffer
	r := &runner{stderr: &stderr}
	if code := r.run([]string{"--repo", repo, "--target-os", "linux"}, env, &errWriter{}); code != 1 || !strings.Contains(stderr.String(), "write regression proof status") {
		t.Fatalf("lost partition status error: code=%d stderr=%s", code, &stderr)
	}
	stderr.Reset()
	if code := r.run([]string{"--repo", t.TempDir(), "--target-os", "linux"}, env, &bytes.Buffer{}); code != 1 || !strings.Contains(stderr.String(), "read regression package") {
		t.Fatalf("lost declaration error: code=%d stderr=%s", code, &stderr)
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		stderr.Reset()
		if code := r.run([]string{"--repo", repo, "--target-os", "windows"}, env, &bytes.Buffer{}); code != 1 || !strings.Contains(stderr.String(), "requires a native") {
			t.Fatalf("Windows proof accepted another host: code=%d stderr=%s", code, &stderr)
		}
	}
}

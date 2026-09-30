package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestRunRoutesWindowsRegressionProof(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{
		"buggy/buggy_windows_test.go": "package buggy\nimport \"testing\"\nfunc TestRegressionProof(t *testing.T) {}\n",
	})
	var stdout, stderr bytes.Buffer
	var commandCalls int
	r := &runner{
		stderr: &stderr,
		execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
			commandCalls++
			return nil, errors.New("proof execution attempted")
		},
	}
	env := regressionProofEnv(map[string]string{
		"PR_TITLE": "fix(ci): route native proof", "PR_BASE_SHA": "base", "PR_BODY": regressionProofBody(),
	})
	if code := r.run([]string{"--repo", repo, "--target-os", "linux"}, env, &stdout); code != 0 {
		t.Fatalf("Linux partition returned %d: %s", code, &stderr)
	}
	if commandCalls != 0 || !strings.Contains(stdout.String(), "other required native proof job must verify them") {
		t.Fatalf("Windows proof must be assigned to its required native job: calls=%d, output=%s", commandCalls, &stdout)
	}
	// An ordinary invocation still attempts every declared proof.
	if code := r.run([]string{"--repo", repo}, env, &stdout); code != 1 || commandCalls != 1 {
		t.Fatalf("default invocation silently omitted Windows proof: code=%d, calls=%d, stderr=%s", code, commandCalls, &stderr)
	}
}

func TestRunDeclaredTestRejectsPackageFailureAfterPass(t *testing.T) {
	t.Parallel()
	exitErr := &exec.ExitError{}
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("{\"Action\":\"pass\",\"Package\":\"example.com/buggy\",\"Test\":\"TestRegressionProof\"}\n"), exitErr
	}}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	if err := r.expectPass(context.Background(), ".", "example.com/buggy", declaration); !errors.Is(err, exitErr) {
		t.Fatalf("passing test with failing package must reject the proof: %v", err)
	}
}

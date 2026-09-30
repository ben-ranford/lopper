package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestBaseFailureOutputRetainsCompleteCaptureSafely(t *testing.T) {
	t.Parallel()

	const raw = "{\"Action\":\"fail\",\"Package\":\"example.com/pkg\",\"Test\":\"TestThing\"}\n" +
		"::error::literal test output\r::notice::still literal\n\ntext file busy\x00\n" +
		"embedded ##[error]legacy command and ##[add-mask]literal value with \\x23 escape\n"
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte(raw), &exec.ExitError{}
	}}
	var output bytes.Buffer
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}
	if err := r.expectFailure(context.Background(), ".", "example.com/pkg", declaration, &output); err != nil {
		t.Fatalf("expected failure: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if lines[0] != "Expected base failure: ./pkg::TestThing" {
		t.Fatalf("missing declaration identity: %q", lines[0])
	}
	var recovered []string
	for _, line := range lines[1:] {
		quoted, ok := strings.CutPrefix(line, "base-test-output: ")
		if !ok || strings.ContainsAny(line, "\r\x00") || strings.Contains(line, "##[") {
			t.Fatalf("unsafe output line: %q", line)
		}
		decoded, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatalf("decode captured line: %v", err)
		}
		recovered = append(recovered, decoded)
	}
	if got := strings.Join(recovered, "\n"); got != raw {
		t.Fatalf("captured output changed: got %q, want %q", got, raw)
	}
}

func TestBaseFailureOutputRequiresValidatedDeclaredFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "pass", output: `{"Action":"pass","Package":"example.com/pkg","Test":"TestThing"}`},
		{name: "skip", output: `{"Action":"skip","Package":"example.com/pkg","Test":"TestThing"}`},
		{name: "no outcome", output: `{"Action":"run","Package":"example.com/pkg","Test":"TestThing"}`},
		{name: "invalid json", output: `{"Action":`},
		{name: "transport", output: `{"Action":"fail","Package":"example.com/pkg","Test":"TestThing"}`, err: errors.New("transport failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
				return []byte(test.output), test.err
			}}
			var output bytes.Buffer
			declaration := prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}
			if err := r.expectFailure(context.Background(), ".", "example.com/pkg", declaration, &output); err == nil {
				t.Fatal("invalid base result accepted")
			}
			if output.Len() != 0 {
				t.Fatalf("invalid result emitted validated failure evidence: %q", &output)
			}
		})
	}
}

func assertProofOutputFailure(t *testing.T, repo regressionProofRepo, declaration prmetadata.RegressionDeclaration, prefix string) {
	t.Helper()

	writeErr := errors.New("diagnostic writer failed")
	var output bytes.Buffer
	writer := proofOutputWriterFunc(func(data []byte) (int, error) {
		if bytes.HasPrefix(data, []byte(prefix)) {
			return 0, writeErr
		}
		return output.Write(data)
	})
	var headTests int
	r := &runner{execCommand: func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if dir == repo.path && strings.Contains(strings.Join(args, " "), " -json ") {
			headTests++
		}
		return (&execRunner{}).Run(ctx, name, args, dir, env)
	}}
	if err := r.prove(context.Background(), repo.path, repo.baseSHA, []prmetadata.RegressionDeclaration{declaration}, writer); !errors.Is(err, writeErr) {
		t.Fatalf("proof error = %v, want writer failure", err)
	}
	if prefix != "Regression proof verified:" && headTests != 0 {
		t.Fatal("head test ran after failed base evidence write")
	}
	if strings.Contains(output.String(), "Regression proof verified:") {
		t.Fatalf("proof reported success after writer failure: %q", &output)
	}
	if worktrees := runRepoCommand(t, repo.path, "git", "worktree", "list", "--porcelain"); strings.Count(worktrees, "worktree ") != 1 {
		t.Fatalf("base worktree not cleaned up: %s", worktrees)
	}
}

type proofOutputWriterFunc func([]byte) (int, error)

func (w proofOutputWriterFunc) Write(data []byte) (int, error) { return w(data) }

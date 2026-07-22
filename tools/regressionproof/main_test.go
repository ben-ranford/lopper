package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/prmetadata"
)

func TestRunRejectsInvalidRegressionProofs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario regressionProofScenario
		wantErr  string
	}{
		{
			name: "missing changed regression files",
			scenario: regressionProofScenario{
				baseFiles: map[string]string{
					"go.mod":         "module example.com/regressionproof\n\ngo 1.23\n",
					"buggy/buggy.go": "package buggy\n\nfunc Fixed() bool { return false }\n",
				},
				headFiles: map[string]string{
					"buggy/buggy.go": "package buggy\n\nfunc Fixed() bool { return true }\n",
				},
			},
			wantErr: "requires at least one changed *_test.go file, package testdata fixture, or shared testdata fixture",
		},
		{
			name: "base unexpectedly passes",
			scenario: regressionProofScenario{
				baseFiles: map[string]string{
					"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
					"buggy/buggy.go": `package buggy

func Fixed() bool { return true }
`,
				},
				headFiles: map[string]string{
					"buggy/buggy_test.go": `package buggy

import "testing"

func TestRegressionProof(t *testing.T) {
	if !Fixed() {
		t.Fatal("expected true")
	}
}
`,
				},
			},
			wantErr: "unexpectedly passed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRegressionProofRepo(t, tt.scenario)
			code, stderr := runRegressionProof(t, repo, regressionProofInvocation{title: "fix(buggy): add proof gate", body: regressionProofBody()})
			if code != 1 {
				t.Fatalf("run exited with %d, want 1 (stderr: %s)", code, stderr)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Fatalf("stderr = %q, want %q", stderr, tt.wantErr)
			}
		})
	}
}

func TestRunFailsWhenBaseDoesNotCompile(t *testing.T) {
	t.Parallel()

	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
		},
		headFiles: map[string]string{
			"buggy/buggy.go": `package buggy

func Fixed() bool { return true }

func NewBehavior() bool { return true }
`,
			"buggy/buggy_test.go": `package buggy

import "testing"

func TestRegressionProof(t *testing.T) {
	if !NewBehavior() {
		t.Fatal("expected new behavior")
	}
}
`,
		},
	})

	code, stderr := runRegressionProof(t, repo, regressionProofInvocation{title: "fix(buggy): add proof gate", body: regressionProofBody()})
	if code != 1 {
		t.Fatalf("run exited with %d, want 1 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "must compile before proof") {
		t.Fatalf("stderr = %q, want compile failure", stderr)
	}
}

func TestRunAcceptsValidRegressionProof(t *testing.T) {
	t.Parallel()

	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
		},
		headFiles: map[string]string{
			"buggy/buggy.go": `package buggy

func Fixed() bool { return true }
`,
			"buggy/buggy_test.go": `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionProof(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "expected.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	want := strings.TrimSpace(string(data)) == "true"
	if got := Fixed(); got != want {
		t.Fatalf("Fixed() = %v, want %v", got, want)
	}
}
`,
			"buggy/testdata/expected.txt": "true\n",
		},
	})

	code, stderr := runRegressionProof(t, repo, regressionProofInvocation{title: "fix(buggy): add proof gate", body: regressionProofBody()})
	if code != 0 {
		t.Fatalf("run exited with %d, want 0 (stderr: %s)", code, stderr)
	}
}

func TestRunAcceptsSharedTopLevelFixtureProof(t *testing.T) {
	t.Parallel()

	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
			"testdata/shared/expected.txt": "false\n",
		},
		headFiles: map[string]string{
			"buggy/buggy.go": `package buggy

func Fixed() bool { return true }
`,
			"buggy/buggy_test.go": `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionProof(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "shared", "expected.txt"))
	if err != nil {
		t.Fatalf("read shared fixture: %v", err)
	}
	want := strings.TrimSpace(string(data)) == "true"
	if got := Fixed(); got != want {
		t.Fatalf("Fixed() = %v, want %v", got, want)
	}
}
`,
			"testdata/shared/expected.txt": "true\n",
		},
	})

	code, stderr := runRegressionProof(t, repo, regressionProofInvocation{title: "fix(buggy): prove shared fixture", body: regressionProofBody()})
	if code != 0 {
		t.Fatalf("run exited with %d, want 0 (stderr: %s)", code, stderr)
	}
}

func TestCommandErrorFormattingAndUnwrap(t *testing.T) {
	t.Parallel()

	errWithOutput := &commandError{name: "go", args: []string{"test"}, output: []byte("boom\n"), err: errors.New("exit")}
	if got := errWithOutput.Error(); !strings.Contains(got, "boom") {
		t.Fatalf("Error() = %q, want embedded output", got)
	}
	if !errors.Is(errWithOutput, errWithOutput.err) {
		t.Fatal("commandError must unwrap to the underlying error")
	}

	errWithoutOutput := &commandError{name: "go", args: []string{"test"}, err: errors.New("exit")}
	if got := errWithoutOutput.Error(); strings.Contains(got, "boom") {
		t.Fatalf("Error() = %q, want no stale output", got)
	}
}

func TestReadBodyUsesEnvAndRejectsOversizedFile(t *testing.T) {
	t.Parallel()

	body, err := readBody("", regressionProofEnv(map[string]string{"PR_BODY": "from env"}))
	if err != nil || body != "from env" {
		t.Fatalf("readBody env = %q, %v", body, err)
	}

	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1<<20+1)), 0o600); err != nil {
		t.Fatalf("write oversized body: %v", err)
	}
	if _, err := readBody(path, func(string) string { return "" }); err == nil {
		t.Fatal("readBody succeeded for oversized body file")
	}
}

func TestRunnerRunHandlesInvalidFlagsAndMissingBody(t *testing.T) {
	t.Parallel()

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	if code := r.run([]string{"--bad-flag"}, func(string) string { return "" }, &bytes.Buffer{}); code != 2 {
		t.Fatalf("run returned %d, want 2", code)
	}
	if code := r.run([]string{"--body-file", filepath.Join(t.TempDir(), "missing.md")}, func(string) string { return "" }, &bytes.Buffer{}); code != 1 {
		t.Fatalf("run returned %d, want 1 for missing body file", code)
	}
	var stderr bytes.Buffer
	r.stderr = &stderr
	if code := r.run([]string{"--regression-exempt-label", "$(touch injected)"}, func(string) string { return "" }, &bytes.Buffer{}); code != 1 {
		t.Fatalf("run returned %d, want 1 for malformed exemption label boolean", code)
	}
	if !strings.Contains(stderr.String(), "must be exactly true or false") {
		t.Fatalf("stderr = %q, want strict boolean validation failure", stderr.String())
	}
}

func TestRunnerRunRequiresReasonAndMaintainerLabelForExemption(t *testing.T) {
	t.Parallel()

	reason := "Regression-Test-Exemption: no deterministic reproducer"
	tests := []struct {
		name       string
		title      string
		body       string
		labelValue string
		wantCode   int
		wantText   string
	}{
		{name: "reason only", title: "fix(ci): add gate", body: reason, labelValue: "false", wantCode: 1, wantText: "maintainer-controlled regression-exempt label"},
		{name: "label only", title: "fix(ci): add gate", body: "## Validation\n\nNo regression metadata\n", labelValue: "true", wantCode: 1, wantText: "requires a non-empty Regression-Test-Exemption reason"},
		{name: "reason and label", title: "fix(ci): add gate", body: reason, labelValue: "true", wantCode: 0, wantText: "Regression proof exempted"},
		{name: "non-fix metadata", title: "feat(ci): add gate", body: reason, labelValue: "true", wantCode: 1, wantText: "only allowed on conventional fix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			r := &runner{stderr: &stderr, execCommand: (&execRunner{}).Run}
			env := regressionProofEnv(map[string]string{
				"PR_TITLE":                   tt.title,
				"PR_BODY":                    tt.body,
				"PR_REGRESSION_EXEMPT_LABEL": tt.labelValue,
			})
			code := r.run(nil, env, &stdout)
			if code != tt.wantCode {
				t.Fatalf("run returned %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			output := stdout.String() + stderr.String()
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output = %q, want %q", output, tt.wantText)
			}
		})
	}
}

func TestParseDeclaredTestAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  string
		test    string
		want    testAction
		wantErr string
	}{
		{
			name:   "pass",
			test:   "TestThing",
			output: "{\"Action\":\"run\",\"Test\":\"TestThing\"}\n{\"Action\":\"pass\",\"Test\":\"TestThing\"}\n",
			want:   testActionPass,
		},
		{
			name:   "fail",
			test:   "TestThing",
			output: "{\"Action\":\"fail\",\"Test\":\"TestThing\"}\n",
			want:   testActionFail,
		},
		{
			name:   "skip",
			test:   "TestThing",
			output: "{\"Action\":\"skip\",\"Test\":\"TestThing\"}\n",
			want:   testActionSkip,
		},
		{
			name:    "missing outcome",
			test:    "TestThing",
			output:  "{\"Action\":\"pass\",\"Test\":\"OtherTest\"}\n",
			wantErr: "did not report an outcome",
		},
		{
			name:    "non json output",
			test:    "TestThing",
			output:  "plain text failure\n",
			wantErr: "did not emit JSON output",
		},
		{
			name:    "invalid json",
			test:    "TestThing",
			output:  "{broken\n",
			wantErr: "parse go test json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDeclaredTestAction([]byte(tt.output), tt.test)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseDeclaredTestAction error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDeclaredTestAction returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseDeclaredTestAction = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunnerRunSkipsNonFixAndRejectsMissingBase(t *testing.T) {
	t.Parallel()

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	stdout := &bytes.Buffer{}
	nonFixEnv := regressionProofEnv(map[string]string{
		"PR_TITLE": "feat(ci): add gate",
		"PR_BODY":  "## Validation\n\nNo regression metadata\n",
	})
	code := r.run(nil, nonFixEnv, stdout)
	if code != 0 || !strings.Contains(stdout.String(), "skipped") {
		t.Fatalf("non-fix run = %d, stdout = %q", code, stdout.String())
	}

	missingBaseEnv := regressionProofEnv(map[string]string{
		"PR_TITLE": "fix(ci): add gate",
		"PR_BODY":  "Regression-Test: ./pkg::TestThing",
	})
	code = r.run(nil, missingBaseEnv, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for missing base SHA", code)
	}
}

func TestRunnerRunRejectsMalformedMetadataAndWriterFailures(t *testing.T) {
	t.Parallel()

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	malformedEnv := regressionProofEnv(map[string]string{
		"PR_TITLE": "fix(ci): add gate",
		"PR_BODY":  "Regression-Test: invalid",
	})
	code := r.run(nil, malformedEnv, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for malformed metadata", code)
	}

	nonFixStatusEnv := regressionProofEnv(map[string]string{
		"PR_TITLE": "feat(ci): add gate",
		"PR_BODY":  "## Validation\n\nNo regression metadata\n",
	})
	code = r.run(nil, nonFixStatusEnv, &errWriter{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for stdout write failure", code)
	}

	exemptionStatusEnv := regressionProofEnv(map[string]string{
		"PR_TITLE":                   "fix(ci): add gate",
		"PR_BODY":                    "Regression-Test-Exemption: no deterministic reproducer",
		"PR_REGRESSION_EXEMPT_LABEL": "true",
	})
	code = r.run(nil, exemptionStatusEnv, &errWriter{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for exemption write failure", code)
	}

	stderrWriteEnv := regressionProofEnv(map[string]string{
		"PR_TITLE":    "fix(ci): add gate",
		"PR_BODY":     "Regression-Test: ./pkg::TestThing",
		"PR_BASE_SHA": "base",
	})
	code = r.run(nil, stderrWriteEnv, &errWriter{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for stderr write failure", code)
	}

	validationEnv := regressionProofEnv(map[string]string{
		"PR_TITLE": "feat(ci): add gate",
		"PR_BODY":  "Regression-Test: ./pkg::TestThing",
	})
	code = r.run(nil, validationEnv, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for validation rejection", code)
	}
}

func TestRunnerRunProofAndAbsFailures(t *testing.T) {
	originalAbsPath := absPath
	defer func() { absPath = originalAbsPath }()

	r := &runner{
		stderr: &bytes.Buffer{},
		execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
			return nil, errors.New("proof failed")
		},
	}
	proofFailureEnv := regressionProofEnv(map[string]string{
		"PR_TITLE":    "fix(ci): add gate",
		"PR_BODY":     "Regression-Test: ./pkg::TestThing",
		"PR_BASE_SHA": "base",
	})
	code := r.run(nil, proofFailureEnv, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for proof failure", code)
	}

	absPath = func(string) (string, error) {
		return "", errors.New("abs failed")
	}
	r = &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	absFailureEnv := regressionProofEnv(map[string]string{
		"PR_TITLE":    "fix(ci): add gate",
		"PR_BODY":     "Regression-Test: ./pkg::TestThing",
		"PR_BASE_SHA": "base",
	})
	code = r.run(nil, absFailureEnv, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run returned %d, want 1 for abs failure", code)
	}
}

func TestChangedFilesAndRunGitErrorPaths(t *testing.T) {
	originalResolve := resolveGitBinaryPath
	resolveGitBinaryPath = func() (string, error) { return "", errors.New("no git") }
	defer func() { resolveGitBinaryPath = originalResolve }()

	r := &runner{stderr: &bytes.Buffer{}, execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		return nil, nil
	}}
	if _, err := r.runGit(context.Background(), ".", "status"); err == nil {
		t.Fatal("runGit succeeded without a git binary")
	}

	resolveGitBinaryPath = originalResolve
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("ResolveBinaryPath: %v", err)
	}
	r.execCommand = func(_ context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if name != gitPath {
			t.Fatalf("name = %q, want git path %q", name, gitPath)
		}
		if len(env) == 0 || dir != "." {
			t.Fatalf("unexpected runGit call: dir=%q env=%d", dir, len(env))
		}
		return []byte("pkg/a_test.go\npkg/testdata/case.txt\n\n"), nil
	}
	files, err := r.changedFiles(context.Background(), ".", "base")
	if err != nil {
		t.Fatalf("changedFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("changedFiles = %#v, want 2 files", files)
	}

	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return nil, errors.New("diff failed")
	}
	if _, err := r.changedFiles(context.Background(), ".", "base"); err == nil {
		t.Fatal("changedFiles succeeded when git diff failed")
	}

	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("\n \n"), nil
	}
	files, err = r.changedFiles(context.Background(), ".", "base")
	if err != nil {
		t.Fatalf("changedFiles returned error for empty diff: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("changedFiles = %#v, want no files", files)
	}
}

func TestCreateBaseWorktreeFailureAndCleanup(t *testing.T) {
	originalResolve := resolveGitBinaryPath
	originalRemoveAll := removeAll
	originalMkdirTemp := mkdirTemp
	defer func() {
		resolveGitBinaryPath = originalResolve
		removeAll = originalRemoveAll
		mkdirTemp = originalMkdirTemp
	}()

	mkdirTemp = func(string, string) (string, error) {
		return "", errors.New("mkdir failed")
	}
	r := &runner{stderr: &bytes.Buffer{}, execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		return nil, nil
	}}
	if _, _, err := r.createBaseWorktree(context.Background(), ".", "base"); err == nil {
		t.Fatal("createBaseWorktree succeeded despite tempdir failure")
	}

	mkdirTemp = originalMkdirTemp

	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("ResolveBinaryPath: %v", err)
	}
	resolveGitBinaryPath = func() (string, error) { return gitPath, nil }

	var removed []string
	removeAll = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	r = &runner{stderr: &bytes.Buffer{}, execCommand: func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "worktree add") {
			return nil, errors.New("add failed")
		}
		return nil, nil
	}}
	if _, _, err := r.createBaseWorktree(context.Background(), ".", "base"); err == nil {
		t.Fatal("createBaseWorktree succeeded on add failure")
	}
	if len(removed) != 1 {
		t.Fatalf("removeAll calls = %d, want 1", len(removed))
	}

	removeAll = func(path string) error {
		removed = append(removed, path)
		return errors.New("cleanup add failed")
	}
	if _, _, err := r.createBaseWorktree(context.Background(), ".", "base"); err == nil {
		t.Fatal("createBaseWorktree succeeded on add failure with cleanup error")
	}

	removeAll = func(path string) error {
		removed = append(removed, path)
		return errors.New("cleanup failed")
	}
	r.execCommand = func(_ context.Context, _ string, _ []string, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	worktreePath, cleanup, err := r.createBaseWorktree(context.Background(), ".", "base")
	if err != nil {
		t.Fatalf("createBaseWorktree returned error: %v", err)
	}
	if !strings.HasSuffix(worktreePath, "base") {
		t.Fatalf("worktreePath = %q", worktreePath)
	}
	if err := cleanup(); err == nil {
		t.Fatal("cleanup succeeded despite removeAll failure")
	}

	removeAll = func(string) error { return nil }
	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "worktree remove") {
			return nil, errors.New("remove worktree failed")
		}
		return nil, nil
	}
	_, cleanup, err = r.createBaseWorktree(context.Background(), ".", "base")
	if err != nil {
		t.Fatalf("createBaseWorktree returned error: %v", err)
	}
	if err := cleanup(); err == nil {
		t.Fatal("cleanup succeeded despite worktree remove failure")
	}
}

func TestCopyProofFilesAndExpectHelpers(t *testing.T) {
	t.Parallel()

	headRoot := t.TempDir()
	baseRoot := t.TempDir()
	sourcePath := filepath.Join(headRoot, "pkg", "case_test.go")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := copyProofFiles(headRoot, baseRoot, []string{"pkg/case_test.go"}); err != nil {
		t.Fatalf("copyProofFiles returned error: %v", err)
	}
	targetData, err := os.ReadFile(filepath.Join(baseRoot, "pkg", "case_test.go"))
	if err != nil || string(targetData) != "package pkg\n" {
		t.Fatalf("copied file = %q, %v", string(targetData), err)
	}

	dirHead := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirHead, "pkg", "dir_test.go"), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := copyProofFiles(dirHead, t.TempDir(), []string{"pkg/dir_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded for non-regular source")
	}

	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	if err := copyProofFiles(headRoot, baseFile, []string{"pkg/case_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded for non-directory base root")
	}
	if err := copyProofFiles(headRoot, baseRoot, []string{"pkg/missing_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded for missing source file")
	}
	if err := copyProofFiles(headRoot, filepath.Join(baseRoot, "missing", "child"), []string{"pkg/case_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded for missing base root")
	}

	r := &runner{stderr: &bytes.Buffer{}, execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		return nil, errors.New("boom")
	}}
	if err := r.expectFailure(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err == nil {
		t.Fatal("expectFailure succeeded for non-exit error")
	}
	if err := r.expectPass(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err == nil {
		t.Fatal("expectPass succeeded for failing command")
	}

	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("{\"Action\":\"fail\",\"Test\":\"TestThing\"}\n"), &exec.ExitError{}
	}
	if err := r.expectFailure(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err != nil {
		t.Fatalf("expectFailure returned error for exit failure: %v", err)
	}

	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("{\"Action\":\"skip\",\"Test\":\"TestThing\"}\n"), nil
	}
	if err := r.expectFailure(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err == nil || !strings.Contains(err.Error(), "must fail instead of skip") {
		t.Fatalf("expectFailure error = %v, want skipped-test rejection", err)
	}
	if err := r.expectPass(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err == nil || !strings.Contains(err.Error(), "must pass instead of skip") {
		t.Fatalf("expectPass error = %v, want skipped-test rejection", err)
	}

	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("{\"Action\":\"run\",\"Test\":\"TestThing\"}\n"), nil
	}
	if err := r.expectFailure(context.Background(), ".", prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}); err == nil || !strings.Contains(err.Error(), "did not report an outcome") {
		t.Fatalf("expectFailure error = %v, want unrecognized outcome rejection", err)
	}
}

func TestRunDeclaredTestBranches(t *testing.T) {
	t.Parallel()

	declaration := prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestThing"}
	r := &runner{stderr: &bytes.Buffer{}}

	parseFailure := errors.New("transport failed")
	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("plain text\n"), parseFailure
	}
	if _, err := r.runDeclaredTest(context.Background(), ".", declaration); !errors.Is(err, parseFailure) || !strings.Contains(err.Error(), "did not emit JSON output") {
		t.Fatalf("runDeclaredTest error = %v, want joined transport and parse errors", err)
	}

	commandFailure := errors.New("exec failed")
	r.execCommand = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte("{\"Action\":\"pass\",\"Test\":\"TestThing\"}\n"), commandFailure
	}
	if _, err := r.runDeclaredTest(context.Background(), ".", declaration); !errors.Is(err, commandFailure) {
		t.Fatalf("runDeclaredTest error = %v, want command failure", err)
	}
}

func TestCopyProofFilesInjectedFailures(t *testing.T) {
	originalOpenWriteRoot := openWriteRoot
	originalReadFileUnder := readFileUnder
	originalStatFile := statFile
	defer func() {
		openWriteRoot = originalOpenWriteRoot
		readFileUnder = originalReadFileUnder
		statFile = originalStatFile
	}()

	headRoot := t.TempDir()
	sourcePath := filepath.Join(headRoot, "pkg", "case_test.go")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	openWriteRoot = func(string) (confinedWriteRoot, error) {
		return nil, errors.New("open failed")
	}
	if err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded despite openWriteRoot failure")
	}

	writeRoot := &stubWriteRoot{}
	openWriteRoot = func(string) (confinedWriteRoot, error) {
		return writeRoot, nil
	}
	assertClosedOnce := func(label string) {
		t.Helper()
		if writeRoot.closeCalls != 1 {
			t.Fatalf("%s close calls = %d, want 1", label, writeRoot.closeCalls)
		}
		writeRoot.closeCalls = 0
	}

	readFailure := errors.New("read failed")
	closeFailure := errors.New("close failed")
	writeRoot.closeErr = closeFailure
	readFileUnder = func(string, string) ([]byte, error) {
		return nil, readFailure
	}
	err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"})
	if !errors.Is(err, readFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("copyProofFiles error = %v, want joined read and close failures", err)
	}
	assertClosedOnce("read failure")

	writeRoot.closeErr = nil
	readFileUnder = func(string, string) ([]byte, error) {
		return []byte("package pkg\n"), nil
	}
	statFailure := errors.New("stat failed")
	statFile = func(string) (fs.FileInfo, error) {
		return nil, statFailure
	}
	if err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"}); !errors.Is(err, statFailure) {
		t.Fatalf("copyProofFiles error = %v, want stat failure", err)
	}
	assertClosedOnce("stat failure")

	statFile = func(string) (fs.FileInfo, error) {
		return &stubFileInfo{mode: 0o644}, nil
	}
	writeFailure := errors.New("write failed")
	writeRoot.writeErr = writeFailure
	if err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"}); !errors.Is(err, writeFailure) {
		t.Fatalf("copyProofFiles error = %v, want write failure", err)
	}
	assertClosedOnce("write failure")

	writeRoot.writeErr = nil
	statFile = func(string) (fs.FileInfo, error) {
		return &stubFileInfo{mode: os.ModeDir | 0o755}, nil
	}
	if err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"}); err == nil {
		t.Fatal("copyProofFiles succeeded despite non-regular source")
	}
	assertClosedOnce("non-regular source")

	statFile = func(string) (fs.FileInfo, error) {
		return &stubFileInfo{mode: 0o644}, nil
	}
	writeRoot.closeErr = closeFailure
	if err := copyProofFiles(headRoot, t.TempDir(), []string{"pkg/case_test.go"}); !errors.Is(err, closeFailure) {
		t.Fatalf("copyProofFiles error = %v, want close failure", err)
	}
	assertClosedOnce("close failure")
}

func TestProveErrorBranchesAndWriteError(t *testing.T) {
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("ResolveBinaryPath: %v", err)
	}
	originalResolve := resolveGitBinaryPath
	originalOpenWriteRoot := openWriteRoot
	resolveGitBinaryPath = func() (string, error) { return gitPath, nil }
	defer func() {
		resolveGitBinaryPath = originalResolve
		openWriteRoot = originalOpenWriteRoot
	}()

	r := &runner{stderr: &bytes.Buffer{}, execCommand: func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "merge-base") {
			return []byte(""), nil
		}
		return nil, nil
	}}
	if err := r.prove(context.Background(), ".", "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded with empty merge-base output")
	}

	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "merge-base"):
			return []byte("deadbeef\n"), nil
		case strings.Contains(joined, "diff"):
			return nil, errors.New("diff failed")
		default:
			return nil, nil
		}
	}
	if err := r.prove(context.Background(), ".", "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded despite changedFiles failure")
	}

	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "merge-base"):
			return []byte("deadbeef\n"), nil
		case strings.Contains(joined, "diff"):
			return []byte("pkg/case_test.go\n"), nil
		case strings.Contains(joined, "worktree add"):
			return nil, errors.New("add failed")
		default:
			return nil, nil
		}
	}
	if err := r.prove(context.Background(), ".", "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded despite worktree creation failure")
	}

	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "merge-base"):
			return []byte("deadbeef\n"), nil
		case strings.Contains(joined, "diff"):
			return []byte("pkg/case_test.go\n"), nil
		case strings.Contains(joined, "worktree add"):
			return nil, nil
		case strings.Contains(joined, "worktree remove"):
			return nil, errors.New("cleanup failed")
		case strings.Contains(joined, "go test"):
			return nil, &exec.ExitError{}
		default:
			return nil, errors.New("unexpected args")
		}
	}
	headRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(headRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir head pkg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(headRoot, "pkg", "case_test.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write head file: %v", err)
	}
	if err := r.prove(context.Background(), headRoot, "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded despite cleanup failure")
	}

	openWriteRoot = func(string) (confinedWriteRoot, error) {
		return nil, errors.New("open failed")
	}
	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "merge-base"):
			return []byte("deadbeef\n"), nil
		case strings.Contains(joined, "diff"):
			return []byte("pkg/case_test.go\n"), nil
		case strings.Contains(joined, "worktree add"), strings.Contains(joined, "worktree remove"):
			return nil, nil
		default:
			return nil, errors.New("unexpected args")
		}
	}
	if err := r.prove(context.Background(), headRoot, "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded despite copy failure")
	}
	openWriteRoot = originalOpenWriteRoot

	r.execCommand = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "merge-base"):
			return []byte("deadbeef\n"), nil
		case strings.Contains(joined, "diff"):
			return []byte("pkg/case_test.go\n"), nil
		case strings.Contains(joined, "worktree add"), strings.Contains(joined, "worktree remove"):
			return nil, nil
		case strings.Contains(joined, " -run ^$ "):
			return nil, errors.New("compile failed")
		default:
			return nil, &exec.ExitError{}
		}
	}
	if err := r.prove(context.Background(), headRoot, "base", []prmetadata.RegressionDeclaration{{PackagePath: "./pkg", TestName: "TestThing"}}, &bytes.Buffer{}); err == nil {
		t.Fatal("prove succeeded despite compile failure")
	}

	if got := writeError(&errWriter{}, "ignored"); got != 1 {
		t.Fatalf("writeError = %d, want 1", got)
	}
}

func TestProveFailsWhenHeadTestStillFails(t *testing.T) {
	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
		},
		headFiles: map[string]string{
			"buggy/buggy_test.go": `package buggy

import "testing"

func TestRegressionProof(t *testing.T) {
	if !Fixed() {
		t.Fatal("expected true")
	}
}
`,
		},
	})

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	err := r.prove(context.Background(), repo.path, repo.baseSHA, []prmetadata.RegressionDeclaration{declaration}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("prove succeeded despite failing head test")
	}
	if !strings.Contains(err.Error(), "must pass on head") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProveFailsWhenHeadTestSkips(t *testing.T) {
	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
		},
		headFiles: map[string]string{
			"buggy/buggy.go": `package buggy

func Fixed() bool { return true }
`,
			"buggy/buggy_test.go": `package buggy

import "testing"

func TestRegressionProof(t *testing.T) {
	if !Fixed() {
		t.Fatal("expected true")
	}
	t.Skip("platform-specific")
}
`,
		},
	})

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	err := r.prove(context.Background(), repo.path, repo.baseSHA, []prmetadata.RegressionDeclaration{declaration}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("prove succeeded despite skipped head test")
	}
	if !strings.Contains(err.Error(), "must pass instead of skip on head") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProveFailsWhenStatusWriteFails(t *testing.T) {
	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{
			"go.mod": "module example.com/regressionproof\n\ngo 1.23\n",
			"buggy/buggy.go": `package buggy

func Fixed() bool { return false }
`,
		},
		headFiles: map[string]string{
			"buggy/buggy.go": `package buggy

func Fixed() bool { return true }
`,
			"buggy/buggy_test.go": `package buggy

import "testing"

func TestRegressionProof(t *testing.T) {
	if !Fixed() {
		t.Fatal("expected true")
	}
}
`,
		},
	})

	r := &runner{stderr: &bytes.Buffer{}, execCommand: (&execRunner{}).Run}
	declaration := prmetadata.RegressionDeclaration{PackagePath: "./buggy", TestName: "TestRegressionProof"}
	if err := r.prove(context.Background(), repo.path, repo.baseSHA, []prmetadata.RegressionDeclaration{declaration}, &errWriter{}); err == nil {
		t.Fatal("prove succeeded despite status write failure")
	}
}

func TestMain(t *testing.T) {
	t.Parallel()

	if os.Getenv("LOPPER_REGRESSIONPROOF_HELPER_MAIN") == "1" {
		os.Args = []string{"regressionproof"}
		main()
		return
	}

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("## Validation\n"), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestMain$")
	helperVars := []string{
		"LOPPER_REGRESSIONPROOF_HELPER_MAIN=1",
		"PR_TITLE=feat(ci): add gate",
		"PR_BODY_FILE=" + bodyFile,
	}
	helperEnv := append(os.Environ(), helperVars...)
	command.Env = helperEnv
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helper main failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Regression proof skipped: non-fix PR.") {
		t.Fatalf("helper output = %q", string(output))
	}
}

type regressionProofInvocation struct {
	title string
	body  string
}

func runRegressionProof(t *testing.T, repo regressionProofRepo, invocation regressionProofInvocation) (int, string) {
	t.Helper()

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte(invocation.body), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	env := map[string]string{
		"PR_TITLE":    invocation.title,
		"PR_BASE_SHA": repo.baseSHA,
	}
	code := run([]string{"--repo", repo.path, "--body-file", bodyFile}, regressionProofEnv(env), &stdout, &stderr)
	return code, stderr.String()
}

func regressionProofEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func regressionProofBody() string {
	lines := []string{
		"## Validation",
		"",
		"Regression-Test: ./buggy::TestRegressionProof",
	}
	return strings.Join(lines, "\n")
}

type regressionProofScenario struct {
	baseFiles map[string]string
	headFiles map[string]string
}

type regressionProofRepo struct {
	path    string
	baseSHA string
}

func newRegressionProofRepo(t *testing.T, scenario regressionProofScenario) regressionProofRepo {
	t.Helper()

	repoPath := t.TempDir()
	runRepoCommand(t, repoPath, "git", "init")
	runRepoCommand(t, repoPath, "git", "config", "user.name", "Test User")
	runRepoCommand(t, repoPath, "git", "config", "user.email", "test@example.com")

	writeFiles(t, repoPath, scenario.baseFiles)
	runRepoCommand(t, repoPath, "git", "add", ".")
	runRepoCommand(t, repoPath, "git", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runRepoCommand(t, repoPath, "git", "rev-parse", "HEAD"))

	writeFiles(t, repoPath, scenario.headFiles)
	runRepoCommand(t, repoPath, "git", "add", ".")
	runRepoCommand(t, repoPath, "git", "commit", "-m", "head")

	return regressionProofRepo{path: repoPath, baseSHA: baseSHA}
}

func writeFiles(t *testing.T, repoPath string, files map[string]string) {
	t.Helper()

	for rel, content := range files {
		path := filepath.Join(repoPath, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func runRepoCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	switch name {
	case "git":
		gitPath, err := gitexec.ResolveBinaryPath()
		if err != nil {
			t.Fatalf("resolve git: %v", err)
		}
		commandArgs := append([]string{"-C", dir}, args...)
		command := exec.Command(gitPath, commandArgs...)
		command.Env = gitexec.SanitizedEnv()
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
		}
		return string(output)
	default:
		command := exec.Command(name, args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
		}
		return string(output)
	}
}

type errWriter struct{}

func (*errWriter) Write([]byte) (int, error) {
	return 0, os.ErrClosed
}

type stubWriteRoot struct {
	writeErr   error
	closeErr   error
	closeCalls int
}

func (s *stubWriteRoot) WriteFileCreatingParents(string, []byte, os.FileMode, os.FileMode) error {
	return s.writeErr
}

func (s *stubWriteRoot) Close() error {
	s.closeCalls++
	return s.closeErr
}

type stubFileInfo struct {
	mode os.FileMode
}

func (s *stubFileInfo) Name() string       { return "stub" }
func (s *stubFileInfo) Size() int64        { return 0 }
func (s *stubFileInfo) Mode() os.FileMode  { return s.mode }
func (s *stubFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (s *stubFileInfo) IsDir() bool        { return s.mode.IsDir() }
func (s *stubFileInfo) Sys() any           { return nil }

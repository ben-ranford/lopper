package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/prmetadata"
	"github.com/ben-ranford/lopper/internal/testutil"
)

// These regressions use the pre-existing runner API so the same tests compile
// on the vulnerable base and fail because a forbidden command reaches execution.
func TestProofRejectsUnsupportedGoCommands(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"version"},
		{"tool", "compile", "attacker.go"},
		{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}", "./pkg", "./other"},
		{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}", "./..."},
		{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}", "./pkg/../other"},
		{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}", "-toolexec=attacker"},
		regressionProofGoTestArgs("-count=1", "-json", "-run", "^TestGood|TestOther$", "./pkg"),
		regressionProofGoTestArgs("-count=1", "-json", "-run", "TestGood", "./pkg"),
		regressionProofGoTestArgs("-count=1", "-json", "-run", "^TestGood$", "./pkg", "-exec=attacker"),
		regressionProofGoTestArgs("-c", "-o", filepath.Join(root, "unallocated-binary"), "./pkg"),
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
				calls++
				return nil, nil
			}}
			if _, err := r.runGo(context.Background(), root, args); err == nil || calls != 0 {
				t.Fatalf("forbidden Go request reached execution: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestProofRejectsUnsupportedGitCommands(t *testing.T) {
	root := t.TempDir()
	oid := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"config", "core.hooksPath", "attacker"},
		{"-c", "core.sshCommand=attacker", "fetch", "origin"},
		{"merge-base", "--", "HEAD\n--independent", "HEAD"},
		{"diff", "--name-only", "--diff-filter=ACMR", "HEAD~1..HEAD", "--"},
		{"diff", "--name-only", "--diff-filter=ACMR", oid + "..HEAD", "--", "other"},
		{"worktree", "add", "--detach", filepath.Join(root, "foreign"), oid},
		{"worktree", "remove", "--force", filepath.Join(root, "foreign")},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
				calls++
				return nil, nil
			}}
			if _, err := r.runGit(context.Background(), root, args...); err == nil || calls != 0 {
				t.Fatalf("forbidden Git request reached execution: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestProofRejectsMalformedMergeBaseOutput(t *testing.T) {
	for _, output := range []string{"deadbeef", "HEAD", "--output=attacker", strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40)} {
		t.Run(output, func(t *testing.T) {
			calls := 0
			r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(output), nil
				}
				return nil, nil
			}}
			err := r.prove(context.Background(), t.TempDir(), "HEAD~1", nil, io.Discard)
			if err == nil || calls != 1 || !strings.Contains(err.Error(), "resolve merge base") {
				t.Fatalf("malformed merge-base output reached later command: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestProofOwnsGoExecutableAndEnvironment(t *testing.T) {
	foreign := t.TempDir()
	for key, value := range map[string]string{
		"PATH": foreign, "GOROOT": foreign, "GOENV": filepath.Join(foreign, "goenv"),
		"GOFLAGS": "-toolexec=attacker", "GOTOOLCHAIN": "attacker", "GOWORK": filepath.Join(foreign, "go.work"),
		"CC": "attacker", "CXX": "attacker", "LD_PRELOAD": "attacker", "DYLD_INSERT_LIBRARIES": "attacker",
		"GITHUB_TOKEN": "fixture-not-a-token", "GIT_CONFIG_COUNT": "99", "GOAUTH": "attacker",
	} {
		t.Setenv(key, value)
	}
	calls := 0
	r := &runner{execCommand: func(_ context.Context, name string, _ []string, _ string, env []string) ([]byte, error) {
		calls++
		if !filepath.IsAbs(name) || strings.HasPrefix(name, foreign+string(os.PathSeparator)) {
			t.Errorf("proof selected caller executable: %q", name)
		}
		assertProofEnvironment(t, env, foreign)
		return []byte("example.com/proof/pkg\n"), nil
	}}
	if _, err := r.resolvePackage(context.Background(), t.TempDir(), "./pkg"); err != nil || calls != 1 {
		t.Fatalf("valid package request must still run: calls=%d err=%v", calls, err)
	}
}

func assertProofEnvironment(t *testing.T, env []string, foreign string) {
	t.Helper()
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[strings.ToUpper(key)] = value
	}
	for _, key := range []string{"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "GITHUB_TOKEN", "GIT_CONFIG_COUNT"} {
		if _, found := values[key]; found {
			t.Errorf("caller process capability survived: %s", key)
		}
	}
	for _, key := range []string{"CC", "CXX"} {
		if !filepath.IsAbs(values[key]) || strings.Contains(values[key], "attacker") {
			t.Errorf("compiler must be owned: %s=%q", key, values[key])
		}
	}
	for key, want := range map[string]string{"GOFLAGS": "", "GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOAUTH": "off"} {
		if values[key] != want {
			t.Errorf("%s=%q, want %q", key, values[key], want)
		}
	}
	if values["GOROOT"] == foreign || strings.Contains(values["PATH"], foreign) {
		t.Error("caller toolchain or PATH override survived")
	}
}

func TestProofValidatesDeclarationAtExecutionBoundary(t *testing.T) {
	calls := 0
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		calls++
		return []byte("{}\n"), nil
	}}
	_, err := r.runDeclaredTest(context.Background(), t.TempDir(), "example.com/pkg",
		prmetadata.RegressionDeclaration{PackagePath: "./pkg", TestName: "TestGood|TestOther"})
	if err == nil || calls != 0 {
		t.Fatalf("invalid declaration reached execution: calls=%d err=%v", calls, err)
	}
}

func TestProofDisablesWorktreeHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook fixture uses a POSIX shell")
	}
	repo := newRegressionProofRepo(t, regressionProofScenario{
		baseFiles: map[string]string{"README.md": "base\n"}, headFiles: map[string]string{"README.md": "head\n"},
	})
	hooks := t.TempDir()
	marker := filepath.Join(hooks, "ran")
	// The quoted fixture path is allocated by the test, never a PR operand.
	script := "#!/bin/sh\nprintf ran > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.RunGit(t, repo.path, "config", "core.hooksPath", hooks)
	r := &runner{execCommand: (&execRunner{}).Run}
	_, cleanup, err := r.createBaseWorktree(context.Background(), repo.path, repo.baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("proof worktree ran repository hook: marker stat=%v", err)
	}
}

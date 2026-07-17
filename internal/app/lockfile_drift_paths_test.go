package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const lockfileRunGitErr = "run git"

func TestLockfileDriftAdditionalPathAndWalkBranches(t *testing.T) {
	if _, err := detectLockfileDrift(context.Background(), "\x00", false); err == nil {
		t.Fatalf("expected detectLockfileDrift to reject invalid repo path")
	}

	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("readdir parent: %v", err)
	}
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("remove child: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "child" {
			continue
		}
		if processLockfileDir(context.Background(), child, entry, nil, lockfileWalkState{repoPath: parent}) == nil {
			t.Fatalf("expected removed directory to fail when scanning lockfile drift")
		}
	}

	if got := relativeDir("\x00", filepath.Join(parent, "pkg")); got != filepath.Join(parent, "pkg") {
		t.Fatalf("expected relativeDir to fall back to input dir, got %q", got)
	}
	if got := mergeGitPaths(); len(got) != 0 {
		t.Fatalf("expected mergeGitPaths with no groups to return nil, got %#v", got)
	}
}

func TestGitCommandContextConstructorError(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	execGitCommandContextFn = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("construct git")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})

	if _, err := gitCommandContext(context.Background(), t.TempDir(), "status"); err == nil || !strings.Contains(err.Error(), "construct git") {
		t.Fatalf("expected gitCommandContext to return constructor error, got %v", err)
	}
}

func TestLockfileDriftFilterAmbiguityErrorNamesAffectedPathsAndDrivers(t *testing.T) {
	err := newLockfileDriftFilterAmbiguityError([]gitFilterPathDriver{
		{path: "package.json", driver: "pwn"},
		{path: "nested/package-lock.json", driver: "drv=with.equals"},
	})
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	for _, want := range []string{
		"cannot safely evaluate lockfile drift",
		"package.json (pwn)",
		"nested/package-lock.json (drv=with.equals)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected ambiguity error to contain %q, got %v", want, err)
		}
	}
}

func TestCollectLockfileGitContextReturnsEmptyForNonGitWorktree(t *testing.T) {
	gitContext, err := collectLockfileGitContext(context.Background(), t.TempDir(), []lockfileRule{lockfileRules[0]})
	if err != nil {
		t.Fatalf("collectLockfileGitContext: %v", err)
	}
	if gitContext.hasGitContext || len(gitContext.changedFiles) != 0 {
		t.Fatalf("expected empty git context, got %#v", gitContext)
	}
}

func TestCollectLockfileGitContextIgnoresIrrelevantCandidates(t *testing.T) {
	repo := configureFakeGitRepo(t, "filterdriver")
	writeFile(t, filepath.Join(repo, "README.md"), "hello\n")

	gitContext, err := collectLockfileGitContext(context.Background(), repo, []lockfileRule{lockfileRules[0]})
	if err != nil {
		t.Fatalf("collectLockfileGitContext: %v", err)
	}
	if !gitContext.hasGitContext || len(gitContext.changedFiles) != 0 {
		t.Fatalf("expected empty candidate-only git context, got %#v", gitContext)
	}
}

func TestCollectLockfileGitContextFailsClosedForRelevantCustomFilters(t *testing.T) {
	repo := configureFakeGitRepo(t, "pathscope-filterdriver")
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

	_, err := collectLockfileGitContext(context.Background(), repo, []lockfileRule{lockfileRules[0]})
	if err == nil || !strings.Contains(err.Error(), "cannot safely evaluate lockfile drift") || !strings.Contains(err.Error(), "package.json (pwn)") {
		t.Fatalf("expected relevant filter ambiguity error, got %v", err)
	}
}

func TestCollectLockfileGitContextFailsClosedForMalformedCheckAttr(t *testing.T) {
	repo := configureFakeGitRepo(t, "checkattrwrongfieldcount")
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

	_, err := collectLockfileGitContext(context.Background(), repo, []lockfileRule{lockfileRules[0]})
	if err == nil || !strings.Contains(err.Error(), "parse git check-attr --stdin -z --all output") {
		t.Fatalf("expected malformed check-attr failure, got %v", err)
	}
	if strings.Contains(err.Error(), "run git diff") {
		t.Fatalf("expected malformed check-attr output to abort before git diff, got %v", err)
	}
}

func TestCollectLockfileGitContextScopesTrackedAndUntrackedCommandsToCandidatePaths(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		wantChanged []string
	}{
		{name: "head", mode: "pathscope-head", wantChanged: []string{"package.json"}},
		{name: "unborn head", mode: "pathscope-unborn", wantChanged: []string{"package-lock.json", "package.json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := configureFakeGitRepo(t, tc.mode)
			writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
			writeFile(t, filepath.Join(repo, lockfileName), "{}\n")
			writeFile(t, filepath.Join(repo, "README.md"), "hello\n")

			gitContext, err := collectLockfileGitContext(context.Background(), repo, []lockfileRule{lockfileRules[0]})
			if err != nil {
				t.Fatalf("collectLockfileGitContext: %v", err)
			}
			if !gitContext.hasGitContext {
				t.Fatal("expected git context")
			}
			assertChangedPathsPresent(t, gitContext.changedFiles, tc.wantChanged...)
			if len(gitContext.changedFiles) != len(tc.wantChanged) {
				t.Fatalf("expected only candidate-path changes %#v, got %#v", tc.wantChanged, gitContext.changedFiles)
			}
		})
	}
}

func TestGitActiveFilterPathDriversAndParser(t *testing.T) {
	assignments, err := gitActiveFilterPathDrivers(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("expected empty path set to skip git check-attr, got %v", err)
	}
	if len(assignments) != 0 {
		t.Fatalf("expected no assignments for empty path set, got %#v", assignments)
	}

	repo := configureFakeGitRepo(t, "checkattrfail")
	if _, err := gitActiveFilterPathDrivers(context.Background(), repo, []string{"package.json"}); err == nil || !strings.Contains(err.Error(), "check-attr") {
		t.Fatalf("expected check-attr failure for non-empty path set, got %v", err)
	}

	assignments, err = parseGitCheckAttrFilterPathDrivers([]string{"package.json", "package-lock.json"}, []byte("package.json\x00eol\x00lf\x00package.json\x00filter\x00pwn=drv\x00"))
	if err != nil {
		t.Fatalf("expected valid path-driver output to parse, got %v", err)
	}
	if len(assignments) != 1 || assignments[0].path != "package.json" || assignments[0].driver != "pwn=drv" {
		t.Fatalf("unexpected parsed assignments %#v", assignments)
	}

	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return "", context.Canceled }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })
	if _, err := gitActiveFilterPathDrivers(context.Background(), t.TempDir(), []string{"package.json"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected command construction failure for non-empty path set, got %v", err)
	}
}

func TestConfiguredGitAttributeDriverErrors(t *testing.T) {
	active, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), nil)
	if err != nil || len(active) != 0 {
		t.Fatalf("expected empty assignments to skip config enumeration, got active=%#v err=%v", active, err)
	}

	t.Run("command construction", func(t *testing.T) {
		original := resolveGitBinaryPathFn
		resolveGitBinaryPathFn = func() (string, error) { return "", context.Canceled }
		t.Cleanup(func() { resolveGitBinaryPathFn = original })

		_, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), []gitFilterPathDriver{{path: "package.json", driver: "set"}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected config command construction error, got %v", err)
		}
	})

	t.Run("config execution", func(t *testing.T) {
		originalResolve := resolveGitBinaryPathFn
		originalExec := execGitCommandContextFn
		resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
		execGitCommandContextFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 2"), nil
		}
		t.Cleanup(func() {
			resolveGitBinaryPathFn = originalResolve
			execGitCommandContextFn = originalExec
		})

		_, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), []gitFilterPathDriver{{path: "package.json", driver: "set"}})
		if err == nil || !strings.Contains(err.Error(), "run git config --null --includes --get-regexp") {
			t.Fatalf("expected config execution error, got %v", err)
		}
	})

	t.Run("state probe construction", func(t *testing.T) {
		forcedErr := errors.New("construct state filter probe")
		originalResolve := resolveGitBinaryPathFn
		originalExec := execGitCommandContextFn
		resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
		execGitCommandContextFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
			if isExecutableFilterConfigEnumeration(gitSubcommandArgs(args)) {
				return shellEscapedOutputCommand(ctx, `filter.set.clean\n./helper.sh\000`), nil
			}
			return nil, forcedErr
		}
		t.Cleanup(func() {
			resolveGitBinaryPathFn = originalResolve
			execGitCommandContextFn = originalExec
		})

		_, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), []gitFilterPathDriver{{path: manifestFileName, driver: "set"}})
		if !errors.Is(err, forcedErr) {
			t.Fatalf("expected state filter probe construction error, got %v", err)
		}
	})
}

func TestConfiguredGitAttributeDriversMatchExactNullConfigRecords(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
	configCalls := 0
	execGitCommandContextFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
		configCalls++
		if !isExecutableFilterConfigEnumeration(gitSubcommandArgs(args)) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1"), nil
		}
		return shellEscapedOutputCommand(ctx, `filter.PWN.clean\n./helper.sh\000filter.Foo/Bar.process\n./process-helper\000filter.empty.clean\n\000filter.pwned.clean\n./other-helper\000`), nil
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})

	active, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), []gitFilterPathDriver{
		{path: "a.json", driver: "pwn"},
		{path: "b.json", driver: "PWN"},
		{path: "c.json", driver: "Foo/Bar"},
		{path: "d.json", driver: "foo/bar"},
		{path: "e.json", driver: "empty"},
		{path: "f.json", driver: "pwn.*"},
	})
	if err != nil {
		t.Fatalf("filter configured git attribute drivers: %v", err)
	}
	if len(active) != 2 || active[0].path != "b.json" || active[1].path != "c.json" {
		t.Errorf("expected exact configured assignments with punctuation in input order, got %#v", active)
	}
	if configCalls != 1 {
		t.Errorf("expected one config-only filter command enumeration, got %d config calls", configCalls)
	}
}

func TestConfiguredGitAttributeDriversRejectMalformedNullConfigRecords(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		errContain string
	}{
		{name: "truncated record", output: `filter.PWN.clean\n./helper.sh`, errContain: "truncated"},
		{name: "missing key value separator", output: `filter.PWN.clean\000`, errContain: "key/value separator"},
		{name: "unexpected key", output: `diff.external\n./helper.sh\000`, errContain: "filter command key"},
		{name: "unexpected filter command key", output: `filter.pwn.smudge\n./helper.sh\000`, errContain: "filter command key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalResolve := resolveGitBinaryPathFn
			originalExec := execGitCommandContextFn
			resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
			execGitCommandContextFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
				if !isExecutableFilterConfigEnumeration(gitSubcommandArgs(args)) {
					return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1"), nil
				}
				return shellEscapedOutputCommand(ctx, tc.output), nil
			}
			t.Cleanup(func() {
				resolveGitBinaryPathFn = originalResolve
				execGitCommandContextFn = originalExec
			})

			_, err := filterConfiguredGitAttributeDrivers(context.Background(), t.TempDir(), []gitFilterPathDriver{{path: "package.json", driver: "pwn"}})
			if err == nil || !strings.Contains(err.Error(), tc.errContain) {
				t.Fatalf("expected malformed config output error containing %q, got %v", tc.errContain, err)
			}
		})
	}
}

func TestGitPathUsesNamedFilterDriver(t *testing.T) {
	if _, err := gitexec.ResolveBinaryPath(); err != nil {
		t.Skip("git binary not available")
	}

	t.Run("git command construction failure", testGitPathUsesNamedFilterDriverCommandConstructionFailure)
	t.Run("inactive boolean state", func(t *testing.T) {
		assertGitPathUsesNamedFilterDriver(t, ".gitattributes", manifestFileName+" filter\n", false, false)
	})
	t.Run("explicit special-name clean driver", func(t *testing.T) {
		assertGitPathUsesNamedFilterDriver(t, ".gitattributes", manifestFileName+" filter=set\n", false, true)
	})
	t.Run("explicit special-name process driver from info attributes", func(t *testing.T) {
		assertGitPathUsesNamedFilterDriver(t, filepath.Join(".git", "info", "attributes"), manifestFileName+" filter=set\n", true, true)
	})
	t.Run("marker probe failure is active but generic probe failure is not", testGitPathUsesNamedFilterDriverProbeFailures)
}

func testGitPathUsesNamedFilterDriverCommandConstructionFailure(t *testing.T) {
	sentinel := errors.New("resolve probe git")
	originalResolve := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return "", sentinel }
	t.Cleanup(func() { resolveGitBinaryPathFn = originalResolve })

	active, err := gitPathUsesNamedFilterDriver(context.Background(), t.TempDir(), gitFilterPathDriver{path: manifestFileName, driver: "set"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected git resolution failure, got %v", err)
	}
	if active {
		t.Fatal("expected failed probe command construction to remain inactive")
	}
}

func assertGitPathUsesNamedFilterDriver(t *testing.T, attributePath, attribute string, writeAttributeAfterInit, wantActive bool) {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	if !writeAttributeAfterInit {
		writeFile(t, filepath.Join(repo, attributePath), attribute)
	}
	initGitRepo(t, repo)
	if writeAttributeAfterInit {
		writeFile(t, filepath.Join(repo, attributePath), attribute)
	}

	active, err := gitPathUsesNamedFilterDriver(context.Background(), repo, gitFilterPathDriver{path: manifestFileName, driver: "set"})
	if err != nil {
		t.Fatalf("gitPathUsesNamedFilterDriver: %v", err)
	}
	if active != wantActive {
		t.Fatalf("expected active=%t, got %t", wantActive, active)
	}
}

func testGitPathUsesNamedFilterDriverProbeFailures(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})

	assertGitFilterProbeClassification(t, gitFilterProbeMarker, true, false)
	assertGitFilterProbeClassification(t, "generic-probe-failure", false, true)
}

func assertGitFilterProbeClassification(t *testing.T, probeFailure string, wantActive, wantErr bool) {
	t.Helper()
	execGitCommandContextFn = gitFilterProbeCommandStub(probeFailure)
	active, err := gitPathUsesNamedFilterDriver(context.Background(), t.TempDir(), gitFilterPathDriver{path: manifestFileName, driver: "set"})
	if (err != nil) != wantErr {
		t.Fatalf("expected error=%t for probe output %q, got %v", wantErr, probeFailure, err)
	}
	if active != wantActive {
		t.Fatalf("expected active=%t for probe output %q, got %t", wantActive, probeFailure, active)
	}
}

func gitFilterProbeCommandStub(probeFailure string) func(context.Context, string, ...string) (*exec.Cmd, error) {
	return func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
		subcommand := gitSubcommandArgs(args)
		if len(subcommand) >= 2 && subcommand[0] == "hash-object" && subcommand[1] == "--stdin" {
			return shellEscapedOutputCommand(ctx, "rawhash\n"), nil
		}
		if len(subcommand) >= 1 && subcommand[0] == "hash-object" {
			return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' "$1" >&2; exit 2`, "git-probe", probeFailure), nil
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1"), nil
	}
}

func gitSubcommandArgs(args []string) []string {
	for len(args) > 0 {
		switch args[0] {
		case "-C", "-c":
			if len(args) < 2 {
				return nil
			}
			args = args[2:]
		default:
			return args
		}
	}
	return nil
}

func isExecutableFilterConfigEnumeration(args []string) bool {
	return len(args) == 5 && args[0] == "config" && args[1] == "--null" && args[2] == "--includes" && args[3] == "--get-regexp"
}

func shellEscapedOutputCommand(ctx context.Context, output string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%b' "$1"`, "git-config-output", output)
}

func TestScopedGitPathHelpersHandleEmptyPathsAndFailures(t *testing.T) {
	assertScopedGitPathHelpersReturnEmpty(t)
	assertScopedGitPathHelperExecutionFailures(t)
	assertScopedGitPathHelperConstructionFailures(t)
}

func assertScopedGitPathHelpersReturnEmpty(t *testing.T) {
	t.Helper()

	changed, err := gitChangedFilesForPaths(context.Background(), t.TempDir(), nil)
	if err != nil || len(changed) != 0 {
		t.Fatalf("expected empty scoped changed set, got %#v err=%v", changed, err)
	}
	tracked, err := gitTrackedChangesForPaths(context.Background(), t.TempDir(), nil)
	if err != nil || len(tracked) != 0 {
		t.Fatalf("expected empty scoped tracked set, got %#v err=%v", tracked, err)
	}
	untracked, err := gitUntrackedFilesForPaths(context.Background(), t.TempDir(), nil)
	if err != nil || len(untracked) != 0 {
		t.Fatalf("expected empty scoped untracked set, got %#v err=%v", untracked, err)
	}
	visible, err := gitVisibleFilesForPaths(context.Background(), t.TempDir(), nil)
	if err != nil || len(visible) != 0 {
		t.Fatalf("expected empty scoped visible set, got %#v err=%v", visible, err)
	}
}

func assertScopedGitPathHelperExecutionFailures(t *testing.T) {
	t.Helper()

	cases := []struct {
		name    string
		mode    string
		run     func(string) error
		wantSub string
	}{
		{
			name: "changed files tracked HEAD diff failure",
			mode: "difffail-head",
			run: func(repo string) error {
				_, err := gitChangedFilesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: lockfileRunGitErr,
		},
		{
			name: "changed files visible classification failure",
			mode: "lsfail",
			run: func(repo string) error {
				_, err := gitChangedFilesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: "ls-files",
		},
		{
			name: "changed files untracked classification failure",
			mode: "untrackedlsfail",
			run: func(repo string) error {
				_, err := gitChangedFilesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: "ls-files",
		},
		{
			name: "tracked cached diff failure",
			mode: "difffail-cached",
			run: func(repo string) error {
				_, err := gitTrackedChangesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: lockfileRunGitErr,
		},
		{
			name: "tracked unstaged diff failure",
			mode: "difffail-unstaged",
			run: func(repo string) error {
				_, err := gitTrackedChangesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: lockfileRunGitErr,
		},
		{
			name: "untracked ls-files failure",
			mode: "lsfail",
			run: func(repo string) error {
				_, err := gitUntrackedFilesForPaths(context.Background(), repo, []string{"package.json"})
				return err
			},
			wantSub: "ls-files",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(configureFakeGitRepo(t, tc.mode)); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func assertScopedGitPathHelperConstructionFailures(t *testing.T) {
	t.Helper()

	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return "", context.Canceled }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })
	if _, err := gitTrackedChangesForPaths(context.Background(), t.TempDir(), []string{"package.json"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected scoped tracked command construction failure, got %v", err)
	}
	if _, err := gitDiffNameOnlyForPaths(context.Background(), t.TempDir(), []string{"package.json"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected scoped diff command construction failure, got %v", err)
	}
	if _, err := gitUntrackedFilesForPaths(context.Background(), t.TempDir(), []string{"package.json"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected scoped untracked command construction failure, got %v", err)
	}
	if _, err := gitVisibleFilesForPaths(context.Background(), t.TempDir(), []string{"package.json"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected scoped visible command construction failure, got %v", err)
	}
}

func TestGitDiffNameOnlyForPathsUsesBoundedLiteralBatches(t *testing.T) {
	paths := scopedGitBatchRegressionPaths()
	commands := captureScopedGitPathspecCommands(t, true)

	got, err := gitDiffNameOnlyForPaths(context.Background(), t.TempDir(), paths, "HEAD")
	if err != nil {
		t.Fatalf("gitDiffNameOnlyForPaths: %v", err)
	}
	assertBoundedScopedGitPathspecCommands(t, *commands)
	assertOrderedUniqueGitPaths(t, got, paths)
}

func TestGitUntrackedFilesForPathsUsesBoundedLiteralBatches(t *testing.T) {
	paths := scopedGitBatchRegressionPaths()
	commands := captureScopedGitPathspecCommands(t, true)

	got, err := gitUntrackedFilesForPaths(context.Background(), t.TempDir(), paths)
	if err != nil {
		t.Fatalf("gitUntrackedFilesForPaths: %v", err)
	}
	assertBoundedScopedGitPathspecCommands(t, *commands)
	assertOrderedUniqueGitPaths(t, got, paths)
}

func scopedGitBatchRegressionPaths() []string {
	paths := make([]string, 0, 262)
	longDir := strings.Repeat("segment/", 32)
	for index := 259; index >= 0; index-- {
		paths = append(paths, fmt.Sprintf("pkg-%03d/%spackage.json", index, longDir))
	}
	paths = append(paths, paths[50], ":(glob)pkg/[literal] package.json")
	return paths
}

func captureScopedGitPathspecCommands(t *testing.T, nulOutput bool) *[][]string {
	t.Helper()

	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
	commands := make([][]string, 0)
	execGitCommandContextFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
		paths := literalPathsFromGitCommand(t, args)
		commands = append(commands, paths)
		return gitPathOutputCommand(ctx, orderedUniqueGitPaths(paths), nulOutput), nil
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})
	return &commands
}

func literalPathsFromGitCommand(t *testing.T, args []string) []string {
	t.Helper()

	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		t.Fatalf("expected pathspec separator in git args %#v", args)
	}
	paths := make([]string, 0, len(args)-separator-1)
	for _, pathspec := range args[separator+1:] {
		path, ok := strings.CutPrefix(pathspec, gitLiteralPathPrefix)
		if !ok {
			t.Fatalf("expected literal pathspec, got %q in %#v", pathspec, args)
		}
		paths = append(paths, path)
	}
	return paths
}

func gitPathOutputCommand(ctx context.Context, paths []string, nulOutput bool) *exec.Cmd {
	format := `%s\n`
	if nulOutput {
		format = `%s\000`
	}
	args := []string{"-c", `format="$1"; shift; for path do printf "$format" "$path"; done`, "git-output", format}
	args = append(args, paths...)
	return exec.CommandContext(ctx, "/bin/sh", args...)
}

func assertBoundedScopedGitPathspecCommands(t *testing.T, commands [][]string) {
	t.Helper()

	const (
		maxPaths = 128
		maxBytes = 16 * 1024
	)
	if len(commands) < 2 {
		t.Fatalf("expected multiple bounded git commands, got %d", len(commands))
	}
	for index, paths := range commands {
		if len(paths) > maxPaths {
			t.Errorf("git command %d carried %d pathspecs; max %d", index, len(paths), maxPaths)
		}
		bytes := 0
		for _, path := range paths {
			bytes += len(gitLiteralPathPrefix) + len(path) + 1
		}
		if bytes > maxBytes && len(paths) > 1 {
			t.Errorf("git command %d carried %d pathspec bytes; max %d", index, bytes, maxBytes)
		}
	}
}

func assertOrderedUniqueGitPaths(t *testing.T, got, input []string) {
	t.Helper()

	want := orderedUniqueGitPaths(input)
	if len(got) != len(want) {
		t.Fatalf("expected %d ordered unique paths, got %d", len(want), len(got))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("path %d = %q, want %q", index, got[index], want[index])
		}
	}
}

func orderedUniqueGitPaths(paths []string) []string {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		unique[path] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for path := range unique {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	return ordered
}

func TestParseNULTerminatedGitOutput(t *testing.T) {
	got := parseNULTerminatedGitOutput([]byte("a\x00b\x00\x00"))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected nul-delimited parse result %#v", got)
	}
}

func TestParseNULTerminatedGitFields(t *testing.T) {
	got := parseNULTerminatedGitFields([]byte("a\x00\x00b\x00"))
	want := []string{"a", "", "b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d nul-delimited fields, got %#v", len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected field %q at index %d, got %#v", want[index], index, got)
		}
	}
}

func TestParseGitExecutableFilterConfigRecords(t *testing.T) {
	configured, err := parseGitExecutableFilterConfig([]byte("filter.PWN.clean\n./helper.sh\x00filter.Pwn.Drv+V1.process\nprocess --flag\x00filter.empty.clean\n\x00"))
	if err != nil {
		t.Fatalf("parse executable filter config: %v", err)
	}

	for _, driver := range []string{"PWN", "Pwn.Drv+V1"} {
		if _, ok := configured[driver]; !ok {
			t.Errorf("expected exact driver %q in %#v", driver, configured)
		}
	}
	for _, driver := range []string{"pwn", "PWN.DRV+v1", "empty"} {
		if _, ok := configured[driver]; ok {
			t.Errorf("expected mismatched or empty driver %q to remain absent from %#v", driver, configured)
		}
	}
	if len(configured) != 2 {
		t.Errorf("expected only executable filter drivers, got %#v", configured)
	}
}

func TestParseGitExecutableFilterConfigPrefersLastValueForEachCommand(t *testing.T) {
	configured, err := parseGitExecutableFilterConfig([]byte("filter.pwn.clean\n./helper.sh\x00filter.pwn.clean\n\x00filter.pwn.process\nprocess --flag\x00filter.other.process\nprocess --flag\x00filter.other.process\n\x00"))
	if err != nil {
		t.Fatalf("parse executable filter config: %v", err)
	}

	if _, ok := configured["pwn"]; !ok {
		t.Fatalf("expected later non-empty process command to keep driver executable, got %#v", configured)
	}
	if _, ok := configured["other"]; ok {
		t.Fatalf("expected later empty process override to clear driver executable, got %#v", configured)
	}
	if len(configured) != 1 {
		t.Fatalf("expected only effective executable drivers, got %#v", configured)
	}
}

func TestParseGitCheckAttrFilterPathDrivers(t *testing.T) {
	output := []byte("package.json\x00filter\x00foo.bar\x00package-lock.json\x00eol\x00lf\x00nested/package.json\x00filter\x00foo/bar\x00package.json\x00filter\x00foo.bar\x00")
	assignments, err := parseGitCheckAttrFilterPathDrivers([]string{"package.json", "package-lock.json", "nested/package.json", "package.json"}, output)
	if err != nil {
		t.Fatalf("expected valid check-attr output to parse, got %v", err)
	}
	want := []gitFilterPathDriver{
		{path: "package.json", driver: "foo.bar"},
		{path: "nested/package.json", driver: "foo/bar"},
		{path: "package.json", driver: "foo.bar"},
	}
	if len(assignments) != len(want) {
		t.Fatalf("expected %d path-driver assignments, got %#v", len(want), assignments)
	}
	for index := range want {
		if assignments[index] != want[index] {
			t.Fatalf("expected assignment %#v at index %d, got %#v", want[index], index, assignments)
		}
	}

	assignments, err = parseGitCheckAttrFilterPathDrivers([]string{"package.json", "package-lock.json", "nested/package.json"}, []byte("package.json\x00filter\x00set\x00package-lock.json\x00filter\x00unset\x00nested/package.json\x00filter\x00 \x00"))
	if err != nil {
		t.Fatalf("expected attribute-state filter values to parse, got %v", err)
	}
	want = []gitFilterPathDriver{
		{path: "package.json", driver: "set"},
		{path: "package-lock.json", driver: "unset"},
	}
	if len(assignments) != len(want) {
		t.Fatalf("expected set/unset entries to remain available for config disambiguation, got %#v", assignments)
	}
	for index := range want {
		if assignments[index] != want[index] {
			t.Fatalf("expected assignment %#v at index %d, got %#v", want[index], index, assignments)
		}
	}

	assignments, err = parseGitCheckAttrFilterPathDrivers([]string{"a.json", "package.json"}, []byte("a.json\x00filter\x00\x00package.json\x00filter\x00pwn=drv\x00"))
	if err != nil {
		t.Fatalf("expected empty filter value to preserve later triplets, got %v", err)
	}
	if len(assignments) != 1 || assignments[0].path != "package.json" || assignments[0].driver != "pwn=drv" {
		t.Fatalf("expected empty filter value to preserve later triplets, got %#v", assignments)
	}
}

func TestParseGitCheckAttrFilterPathDriversRejectsMalformedOutput(t *testing.T) {
	cases := []struct {
		name       string
		paths      []string
		output     []byte
		errContain string
	}{
		{
			name:       "truncated output",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00filter"),
			errContain: "truncated output",
		},
		{
			name:       "wrong field count",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00filter\x00foo.bar\x00extra\x00"),
			errContain: "complete NUL-delimited attribute triplets",
		},
		{
			name:       "wrong path",
			paths:      []string{"package.json"},
			output:     []byte("package-lock.json\x00filter\x00foo.bar\x00"),
			errContain: "unexpected attribute path",
		},
		{
			name:       "duplicate filter record",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00filter\x00foo.bar\x00package.json\x00filter\x00foo.baz\x00"),
			errContain: "too many filter records",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGitCheckAttrFilterPathDrivers(tc.paths, tc.output)
			if err == nil || !strings.Contains(err.Error(), tc.errContain) {
				t.Fatalf("expected error containing %q, got %v", tc.errContain, err)
			}
		})
	}
}

func configureFakeGitRepo(t *testing.T, mode string) string {
	t.Helper()

	original := resolveGitBinaryPathFn
	repo := t.TempDir()
	fakeGit := writeFakeGitBinary(t)
	resolveGitBinaryPathFn = func() (string, error) { return fakeGit, nil }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })
	useFakeGitCommandContext(t)
	writeFakeGitMode(t, repo, mode)
	return repo
}

func writeFakeGitBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
args="$*"
mode="${FAKE_GIT_MODE}"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-C" ] && [ -n "$2" ] && [ -f "$2/.fake-git-mode" ]; then
    mode="$(cat "$2/.fake-git-mode")"
    break
  fi
  shift
done
if printf '%s' "$args" | grep -q 'rev-parse --is-inside-work-tree'; then
  echo true
  exit 0
fi
if printf '%s' "$args" | grep -q 'rev-parse --verify --quiet HEAD'; then
  if [ "$mode" = "difffail-cached" ] || [ "$mode" = "difffail-unstaged" ] || [ "$mode" = "pathscope-unborn" ]; then
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'ls-files --others --exclude-standard'; then
  if [ "$mode" = "pathscope-head" ] || [ "$mode" = "pathscope-unborn" ] || [ "$mode" = "pathscope-filterdriver" ] || [ "$mode" = "checkattrtruncated" ] || [ "$mode" = "checkattrwrongfieldcount" ] || [ "$mode" = "checkattrwrongattr" ] || [ "$mode" = "checkattrwrongorder" ]; then
    if ! printf '%s' "$args" | grep -q -- 'ls-files --others --exclude-standard -z -- :(literal)package-lock.json :(literal)package.json'; then
      echo "missing pathspec-scoped untracked args: $args" >&2
      exit 1
    fi
    if [ "$mode" = "pathscope-unborn" ]; then
      printf 'package-lock.json\000'
    fi
    exit 0
  fi
  if [ "$mode" = "lsfail" ] || [ "$mode" = "untrackedlsfail" ]; then
    echo "ls-files failed" >&2
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'ls-files --cached --others --exclude-standard -z'; then
  if [ "$mode" = "lsfail" ]; then
    echo "ls-files failed" >&2
    exit 1
  fi
  if printf '%s' "$args" | grep -q -- ':(literal)package-lock.json' && printf '%s' "$args" | grep -q -- ':(literal)package.json'; then
    printf 'package-lock.json\000package.json\000'
    exit 0
  fi
  if printf '%s' "$args" | grep -q -- ':(literal)package.json'; then
    printf 'package.json\000'
    exit 0
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'check-attr --stdin -z --all'; then
  if [ "$mode" = "pathscope-head" ]; then
    cat >/dev/null
    exit 0
  fi
  if [ "$mode" = "pathscope-unborn" ]; then
    cat >/dev/null
    exit 0
  fi
  if [ "$mode" = "pathscope-filterdriver" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000pwn\000'
    exit 0
  fi
  if [ "$mode" = "lsfail" ]; then
    cat >/dev/null
    exit 0
  fi
  if [ "$mode" = "checkattrfail" ]; then
    echo "check-attr failed" >&2
    exit 1
  fi
  if [ "$mode" = "checkattrtruncated" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongfieldcount" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar\000extra\000'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongattr" ]; then
    cat >/dev/null
    printf 'package.json\000eol\000lf\000'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongorder" ]; then
    cat >/dev/null
    printf 'package-lock.json\000filter\000foo.bar\000package.json\000filter\000foo.baz\000'
    exit 0
  fi
  if [ "$mode" = "filterdriver" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar\000'
    exit 0
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'config --null --includes --get-regexp'; then
  if [ "$mode" = "pathscope-filterdriver" ]; then
    printf 'filter.pwn.clean\n./helper.sh\000'
    exit 0
  fi
  exit 1
fi
if printf '%s' "$args" | grep -q 'diff --no-ext-diff --no-textconv'; then
  if [ "$mode" = "pathscope-head" ]; then
    if ! printf '%s' "$args" | grep -q -- 'diff --no-ext-diff --no-textconv HEAD --name-only -z -- :(literal)package-lock.json :(literal)package.json'; then
      echo "missing pathspec-scoped head diff args: $args" >&2
      exit 1
    fi
    printf 'package.json\000'
    exit 0
  fi
  if [ "$mode" = "pathscope-unborn" ]; then
    if printf '%s' "$args" | grep -q -- '--cached'; then
      if ! printf '%s' "$args" | grep -q -- 'diff --no-ext-diff --no-textconv --cached --name-only -z -- :(literal)package.json'; then
        echo "missing pathspec-scoped cached diff args: $args" >&2
        exit 1
      fi
      printf 'package.json\000'
      exit 0
    fi
    if ! printf '%s' "$args" | grep -q -- 'diff --no-ext-diff --no-textconv --name-only -z -- :(literal)package.json'; then
      echo "missing pathspec-scoped unstaged diff args: $args" >&2
      exit 1
    fi
    exit 0
  fi
  if [ "$mode" = "pathscope-filterdriver" ]; then
    echo "scoped git diff should not run after filter ambiguity" >&2
    exit 1
  fi
  if [ "$mode" = "filterdriver" ]; then
    if printf '%s' "$args" | grep -q -- '--attr-source='; then
      echo "unexpected attr-source flag" >&2
      exit 1
    fi
    for expected in \
      'GIT_CONFIG_KEY_5=filter.foo.bar.clean' \
      'GIT_CONFIG_KEY_6=filter.foo.bar.process' \
      'GIT_CONFIG_KEY_7=filter.foo.bar.required' \
      'GIT_CONFIG_VALUE_7=false'; do
      if ! env | grep -q "$expected"; then
        echo "missing filter override env: $expected" >&2
        exit 1
      fi
    done
    exit 0
  fi
  if [ "$mode" = "checkattrtruncated" ] || [ "$mode" = "checkattrwrongfieldcount" ] || [ "$mode" = "checkattrwrongattr" ] || [ "$mode" = "checkattrwrongorder" ]; then
    echo "git diff should not run after malformed check-attr output" >&2
    exit 1
  fi
  if printf '%s' "$args" | grep -q -- '--cached'; then
    if [ "$mode" = "difffail-cached" ]; then
      echo "diff failed" >&2
      exit 1
    fi
    exit 0
  fi
  if [ "$mode" = "difffail-head" ] || [ "$mode" = "difffail-unstaged" ]; then
    echo "diff failed" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git script: %v", err)
	}
	return path
}

func writeFakeGitMode(t *testing.T, repo, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".fake-git-mode"), []byte(mode), 0o600); err != nil {
		t.Fatalf("write fake git mode: %v", err)
	}
}

func useFakeGitCommandContext(t *testing.T) {
	t.Helper()

	original := execGitCommandContextFn
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, gitPath, args...), nil
	}
	t.Cleanup(func() {
		execGitCommandContextFn = original
	})
}

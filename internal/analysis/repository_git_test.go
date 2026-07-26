package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/workspace"
)

func TestOpenTrustedRepositoryWithGitMetadataSealsSamePathHeadAndConfig(t *testing.T) {
	repo := t.TempDir()
	const configA = "thresholds:\n  low_confidence_warning_percent: 11\n"
	const configB = "thresholds:\n  low_confidence_warning_percent: 77\n"
	writeFile(t, filepath.Join(repo, ".lopper.yml"), configA)
	initRepositoryGitFixture(t, repo)
	commitA, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve commit A: %v", err)
	}

	writeFile(t, filepath.Join(repo, ".lopper.yml"), configB)
	testutil.RunGit(t, repo, "add", ".lopper.yml")
	testutil.RunGit(t, repo, "commit", "-m", "config B")
	commitB, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve commit B: %v", err)
	}
	testutil.RunGit(t, repo, "checkout", "--detach", commitA)

	hookCalls := 0
	restore := SetRepositoryViewHandleOpenedHookForTest(func() error {
		hookCalls++
		testutil.RunGit(t, repo, "checkout", "--detach", commitB)
		return nil
	})
	t.Cleanup(restore)

	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepositoryWithGitMetadata(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open metadata repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	metadata := view.GitMetadata()
	if !metadata.Captured || !metadata.IsWorktree || metadata.CaptureError != nil {
		t.Fatalf("unexpected Git metadata: %#v", metadata)
	}
	if metadata.CurrentCommit != commitA {
		t.Fatalf("captured commit = %q, want commit A %q", metadata.CurrentCommit, commitA)
	}
	if len(metadata.ChangedFiles) != 0 {
		t.Fatalf("captured clean repository as dirty: %#v", metadata.ChangedFiles)
	}
	if hookCalls != 1 {
		t.Fatalf("completed-view hook calls = %d, want 1", hookCalls)
	}
	assertRepositoryConfigContents(t, filepath.Join(view.ExecutionPath(), ".lopper.yml"), configA, "snapshot config retargeted after checkout")
	assertRepositoryConfigContents(t, filepath.Join(repo, ".lopper.yml"), configB, "same-path checkout hook did not install config B")

	metadata.ChangedFiles = append(metadata.ChangedFiles, "mutated")
	if slices.Contains(view.GitMetadata().ChangedFiles, "mutated") {
		t.Fatal("Git metadata accessor exposed mutable view state")
	}
}

func assertRepositoryConfigContents(t *testing.T, path, want, failure string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s: %q", failure, got)
	}
}

func TestOpenTrustedRepositoryWithGitMetadataCapturesDirtyFiltersAndNonGit(t *testing.T) {
	t.Run("dirty filtered worktree", testRepositoryGitDirtyFilteredWorktree)
	t.Run("non Git directory is explicitly captured", testRepositoryGitNonGitDirectoryCaptured)
	var nilView *RepositoryView
	if metadata := nilView.GitMetadata(); metadata.Captured {
		t.Fatalf("nil view returned captured metadata: %#v", metadata)
	}
}

func TestRepositoryGitMetadataCapturesUnbornAndStateNamedFilters(t *testing.T) {
	t.Run("unborn head", func(t *testing.T) {
		repo := t.TempDir()
		testutil.RunGit(t, repo, "init")
		writeFile(t, filepath.Join(repo, "staged.txt"), "staged\n")
		writeFile(t, filepath.Join(repo, "untracked.txt"), "untracked\n")
		testutil.RunGit(t, repo, "add", "staged.txt")

		metadata := captureRepositoryGitMetadata(context.Background(), repo)
		if metadata.CaptureError != nil || !metadata.IsWorktree || metadata.CurrentCommit != "" {
			t.Fatalf("unborn metadata = %#v", metadata)
		}
		if !slices.Contains(metadata.ChangedFiles, "staged.txt") || !slices.Contains(metadata.ChangedFiles, "untracked.txt") {
			t.Fatalf("unborn changed files = %#v", metadata.ChangedFiles)
		}
	})

	for _, tc := range []struct {
		name       string
		attribute  string
		wantActive bool
	}{
		{name: "named state driver", attribute: "*.json filter=set", wantActive: true},
		{name: "boolean attribute state", attribute: "*.json filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, ".gitattributes"), tc.attribute+"\n")
			writeFile(t, filepath.Join(repo, "package.json"), "{}\n")
			initRepositoryGitFixture(t, repo)
			testutil.RunGit(t, repo, "config", "filter.set.clean", "cat")

			metadata := captureRepositoryGitMetadata(context.Background(), repo)
			if metadata.CaptureError != nil {
				t.Fatalf("capture state-named filter metadata: %v", metadata.CaptureError)
			}
			active := slices.Contains(metadata.ActiveFilterDrivers, RepositoryGitFilter{Path: "package.json", Driver: "set"})
			if active != tc.wantActive {
				t.Fatalf("state-named filter active = %t, want %t: %#v", active, tc.wantActive, metadata.ActiveFilterDrivers)
			}
		})
	}
}

func TestRepositoryGitMetadataCaptureStageErrors(t *testing.T) {
	sentinel := errors.New("git metadata sentinel")
	originalBinary := resolveRepositoryGitBinaryPathFn
	originalWorktree := repositoryGitWorktreeFn
	originalExecutableFilters := repositoryExecutableFiltersFn
	originalActiveFilters := repositoryActiveFiltersFn
	originalCurrentCommit := repositoryCurrentCommitFn
	originalChangedFiles := repositoryChangedFilesFn
	t.Cleanup(func() {
		resolveRepositoryGitBinaryPathFn = originalBinary
		repositoryGitWorktreeFn = originalWorktree
		repositoryExecutableFiltersFn = originalExecutableFilters
		repositoryActiveFiltersFn = originalActiveFilters
		repositoryCurrentCommitFn = originalCurrentCommit
		repositoryChangedFilesFn = originalChangedFiles
	})

	reset := func() {
		resolveRepositoryGitBinaryPathFn = func() (string, error) { return gitexec.ExecutablePrimary, nil }
		repositoryGitWorktreeFn = func(context.Context, string, string) (bool, error) { return true, nil }
		repositoryExecutableFiltersFn = func(context.Context, string, string) ([]string, error) { return []string{"demo"}, nil }
		repositoryActiveFiltersFn = func(context.Context, string, string, []string) ([]RepositoryGitFilter, error) {
			return []RepositoryGitFilter{{Path: "package.json", Driver: "demo"}}, nil
		}
		repositoryCurrentCommitFn = func(context.Context, string, string) (string, bool, error) { return "abc", true, nil }
		repositoryChangedFilesFn = func(context.Context, string, string, []string, bool) ([]string, error) {
			return []string{"package.json"}, nil
		}
	}

	for _, tc := range []struct {
		name string
		fail func()
	}{
		{name: "binary", fail: func() {
			resolveRepositoryGitBinaryPathFn = func() (string, error) { return "", sentinel }
		}},
		{name: "worktree", fail: func() {
			repositoryGitWorktreeFn = func(context.Context, string, string) (bool, error) { return false, sentinel }
		}},
		{name: "filters config", fail: func() {
			repositoryExecutableFiltersFn = func(context.Context, string, string) ([]string, error) { return nil, sentinel }
		}},
		{name: "active filters", fail: func() {
			repositoryActiveFiltersFn = func(context.Context, string, string, []string) ([]RepositoryGitFilter, error) { return nil, sentinel }
		}},
		{name: "current commit", fail: func() {
			repositoryCurrentCommitFn = func(context.Context, string, string) (string, bool, error) { return "", false, sentinel }
		}},
		{name: "changed files", fail: func() {
			repositoryChangedFilesFn = func(context.Context, string, string, []string, bool) ([]string, error) { return nil, sentinel }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			tc.fail()
			metadata := captureRepositoryGitMetadata(context.Background(), t.TempDir())
			if !errors.Is(metadata.CaptureError, sentinel) {
				t.Fatalf("capture error = %v, want sentinel", metadata.CaptureError)
			}
		})
	}
}

func TestRepositoryGitMetadataParserAndCommandErrors(t *testing.T) {
	t.Run("parser helpers", testRepositoryGitParserHelpers)
	t.Run("missing and unsupported commands", testRepositoryGitMissingAndUnsupportedCommands)
	t.Run("empty repository and invalid path", testRepositoryGitEmptyRepositoryAndInvalidPath)
}

func TestOpenTrustedRepositoryWithGitMetadataRejectsCaptureAndIdentityDrift(t *testing.T) {
	originalWorktree := repositoryGitWorktreeFn
	originalExecutableFilters := repositoryExecutableFiltersFn
	originalActiveFilters := repositoryActiveFiltersFn
	originalCurrentCommit := repositoryCurrentCommitFn
	originalChangedFiles := repositoryChangedFilesFn
	t.Cleanup(func() {
		repositoryGitWorktreeFn = originalWorktree
		repositoryExecutableFiltersFn = originalExecutableFilters
		repositoryActiveFiltersFn = originalActiveFilters
		repositoryCurrentCommitFn = originalCurrentCommit
		repositoryChangedFilesFn = originalChangedFiles
	})
	reset := func() {
		repositoryGitWorktreeFn = func(context.Context, string, string) (bool, error) { return true, nil }
		repositoryExecutableFiltersFn = func(context.Context, string, string) ([]string, error) { return nil, nil }
		repositoryActiveFiltersFn = func(context.Context, string, string, []string) ([]RepositoryGitFilter, error) { return nil, nil }
		repositoryCurrentCommitFn = func(context.Context, string, string) (string, bool, error) { return "commit-a", true, nil }
	}

	t.Run("Git state", func(t *testing.T) {
		reset()
		repositoryChangedFilesFn = sequenceRepositoryChangedFiles(nil, []string{"state-1"}, []string{"state-2"})
		assertOpenTrustedRepositoryWithGitMetadataError(t, t.TempDir(), "Git state changed")
	})

	t.Run("directory identity", func(t *testing.T) {
		reset()
		parent := t.TempDir()
		repo := filepath.Join(parent, "repo")
		moved := filepath.Join(parent, "moved")
		if err := os.Mkdir(repo, 0o750); err != nil {
			t.Fatalf("mkdir repository: %v", err)
		}
		renameOnSecondCall := renameRepositoryOnSecondCall(repo, moved)
		repositoryChangedFilesFn = func(context.Context, string, string, []string, bool) ([]string, error) {
			if err := renameOnSecondCall(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		assertOpenTrustedRepositoryWithGitMetadataError(t, repo, "repository changed while opening")
	})
}

func testRepositoryGitDirtyFilteredWorktree(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".gitattributes"), "*.json filter=demo\n")
	writeFile(t, filepath.Join(repo, "package.json"), "{}\n")
	writeFile(t, filepath.Join(repo, "second.json"), "{}\n")
	initRepositoryGitFixture(t, repo)
	testutil.RunGit(t, repo, "config", "filter.demo.clean", "cat")
	testutil.RunGit(t, repo, "config", "filter.demo.process", "")
	writeFile(t, filepath.Join(repo, "package.json"), "{\"changed\":true}\n")
	writeFile(t, filepath.Join(repo, "untracked.txt"), "new\n")
	view := openRepositoryGitMetadataView(t, repo)
	metadata := view.GitMetadata()
	if metadata.CaptureError != nil {
		t.Fatalf("capture Git metadata: %v", metadata.CaptureError)
	}
	if !slices.Contains(metadata.ChangedFiles, "package.json") || !slices.Contains(metadata.ChangedFiles, "untracked.txt") {
		t.Fatalf("changed files = %#v", metadata.ChangedFiles)
	}
	if !slices.Contains(metadata.ActiveFilterDrivers, RepositoryGitFilter{Path: "package.json", Driver: "demo"}) {
		t.Fatalf("active filters = %#v", metadata.ActiveFilterDrivers)
	}
}

func testRepositoryGitNonGitDirectoryCaptured(t *testing.T) {
	metadata := openRepositoryGitMetadataView(t, t.TempDir()).GitMetadata()
	if !metadata.Captured || metadata.IsWorktree || metadata.CaptureError != nil {
		t.Fatalf("non-Git metadata = %#v", metadata)
	}
}

func openRepositoryGitMetadataView(t *testing.T, repo string) *RepositoryView {
	t.Helper()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepositoryWithGitMetadata(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open metadata repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})
	return view
}

func testRepositoryGitParserHelpers(t *testing.T) {
	if isAbsoluteLocationPath("") {
		t.Fatal("empty location path reported absolute")
	}
	if cloned := (*RepositoryGitMetadata)(nil).clone(); cloned.Captured {
		t.Fatalf("nil metadata clone = %#v", cloned)
	}
	if errorText(errors.New("sentinel")) != "sentinel" {
		t.Fatal("errorText did not preserve error text")
	}
	if filters, err := parseRepositoryGitFilters(nil, nil); err != nil || len(filters) != 0 {
		t.Fatalf("empty attributes = %#v, %v", filters, err)
	}
	for _, output := range [][]byte{[]byte("path\x00filter\x00demo"), []byte("path\x00filter\x00")} {
		if _, err := parseRepositoryGitFilters(output, map[string]struct{}{"demo": {}}); err == nil {
			t.Fatalf("expected malformed attributes error for %q", output)
		}
	}
	filters, err := parseRepositoryGitFilters([]byte("path\x00diff\x00demo\x00path\x00filter\x00missing\x00"), map[string]struct{}{"demo": {}})
	if err != nil || len(filters) != 0 {
		t.Fatalf("unexpected inert filters: %#v, %v", filters, err)
	}
	for _, key := range []string{"other.demo.clean", "filter.demo.other", "filter..clean"} {
		if driver, ok := repositoryGitFilterDriverFromConfigKey(key); ok || driver != "" {
			t.Fatalf("accepted invalid filter config key %q", key)
		}
	}
}

func testRepositoryGitMissingAndUnsupportedCommands(t *testing.T) {
	repo := t.TempDir()
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git binary: %v", err)
	}
	missingRepo := filepath.Join(repo, "missing")
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "missing worktree", run: func() error {
			_, err := repositoryIsGitWorktree(context.Background(), gitPath, missingRepo)
			return err
		}},
		{name: "missing current commit", run: func() error {
			_, _, err := repositoryCurrentCommit(context.Background(), gitPath, missingRepo)
			return err
		}},
		{name: "missing changed files", run: func() error {
			_, err := repositoryChangedFiles(context.Background(), gitPath, missingRepo, nil, true)
			return err
		}},
		{name: "missing unborn changed files", run: func() error {
			_, err := repositoryChangedFiles(context.Background(), gitPath, missingRepo, nil, false)
			return err
		}},
		{name: "missing active filters", run: func() error {
			_, err := repositoryActiveFilterDrivers(context.Background(), gitPath, missingRepo, []string{"demo"})
			return err
		}},
		{name: "missing filter config", run: func() error {
			_, err := repositoryExecutableFilterDrivers(context.Background(), gitPath, missingRepo)
			return err
		}},
		{name: "unsupported git paths", run: func() error {
			_, err := repositoryGitPaths(context.Background(), "unsupported", repo, nil, "status")
			return err
		}},
		{name: "unsupported worktree", run: func() error { _, err := repositoryIsGitWorktree(context.Background(), "unsupported", repo); return err }},
		{name: "unsupported current commit", run: func() error {
			_, _, err := repositoryCurrentCommit(context.Background(), "unsupported", repo)
			return err
		}},
		{name: "unsupported active filters", run: func() error {
			_, err := repositoryActiveFilterDrivers(context.Background(), "unsupported", repo, []string{"demo"})
			return err
		}},
		{name: "unsupported filter config", run: func() error {
			_, err := repositoryExecutableFilterDrivers(context.Background(), "unsupported", repo)
			return err
		}},
		{name: "unsupported filter probe", run: func() error {
			_, err := repositoryPathUsesNamedFilterDriver(context.Background(), "unsupported", repo, RepositoryGitFilter{Path: "package.json", Driver: "set"})
			return err
		}},
	} {
		if err := tc.run(); err == nil {
			t.Fatalf("expected %s error", tc.name)
		}
	}
	var nilContext context.Context
	if _, err := repositoryGitCommand(nilContext, "unsupported", repo, nil, "status"); err == nil {
		t.Fatal("expected unsupported nil-context Git command error")
	}
	if _, err := repositoryPathUsesNamedFilterDriver(context.Background(), gitPath, repo, RepositoryGitFilter{Path: "\x00", Driver: "set"}); err == nil || !strings.Contains(err.Error(), "probe git filter") {
		t.Fatalf("expected filter probe error, got %v", err)
	}
}

func testRepositoryGitEmptyRepositoryAndInvalidPath(t *testing.T) {
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git binary: %v", err)
	}
	emptyRepo := t.TempDir()
	testutil.RunGit(t, emptyRepo, "init")
	testutil.RunGit(t, emptyRepo, "config", "filter.demo.clean", "cat")
	if filters, err := repositoryActiveFilterDrivers(context.Background(), gitPath, emptyRepo, []string{"demo"}); err != nil || len(filters) != 0 {
		t.Fatalf("empty repository active filters = %#v, %v", filters, err)
	}
	repository, err := ResolveTrustedRepository(emptyRepo)
	if err != nil {
		t.Fatalf("authorize empty repository: %v", err)
	}
	if err := useTrustedRepository("\x00", repository); err == nil {
		t.Fatal("expected invalid repository path rejection")
	}
}

func sequenceRepositoryChangedFiles(callErr error, states ...[]string) func(context.Context, string, string, []string, bool) ([]string, error) {
	call := 0
	return func(context.Context, string, string, []string, bool) ([]string, error) {
		call++
		if callErr != nil {
			return nil, callErr
		}
		if call-1 < len(states) {
			return states[call-1], nil
		}
		return nil, nil
	}
}

func renameRepositoryOnSecondCall(repo, moved string) func() error {
	call := 0
	return func() error {
		call++
		if call != 2 {
			return nil
		}
		if err := os.Rename(repo, moved); err != nil {
			return err
		}
		return os.Mkdir(repo, 0o750)
	}
}

func assertOpenTrustedRepositoryWithGitMetadataError(t *testing.T, repo, wantSubstring string) {
	t.Helper()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	if _, err := OpenTrustedRepositoryWithGitMetadata(context.Background(), repository, repo, nil); err == nil || !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected %s rejection, got %v", wantSubstring, err)
	}
}

func initRepositoryGitFixture(t *testing.T, repo string) {
	t.Helper()
	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "analysis-test@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Analysis Test")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "fixture A")
}

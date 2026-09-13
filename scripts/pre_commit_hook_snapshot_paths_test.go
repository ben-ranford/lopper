package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallSnapshotTempIsAllocatedBeforeCopy(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	if err := os.MkdirAll(managedDir, 0o777); err != nil {
		t.Fatalf("create writable managed directory: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "snapshot-path")
	wrapperDir := snapshotPathWrapper(t, marker, "*.pre-commit.tmp.*")
	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("hooks-install = %v\n%s", err, output)
	}
	assertSnapshotPathObserved(t, marker)
	info, err := os.Lstat(managedHookPath(t, repoDir))
	if err != nil {
		t.Fatalf("lstat installed hook: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("installed hook mode = %v, want regular file", info.Mode())
	}
}

func TestHooksInstallRejectsSymlinkedReviewedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	sourceHook := filepath.Join(repoDir, ".githooks", "pre-commit")
	victim := filepath.Join(t.TempDir(), "unreviewed-hook")
	writeFileMode(t, victim, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.Remove(sourceHook); err != nil {
		t.Fatalf("remove reviewed hook fixture: %v", err)
	}
	if err := os.Symlink(victim, sourceHook); err != nil {
		t.Fatalf("symlink reviewed hook fixture: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Missing or unsafe reviewed hook") {
		t.Fatalf("hooks-install with symlinked reviewed hook = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("symlinked source created managed hook state: %v", err)
	}
}

func TestHooksInstallRejectsSymlinkedReviewedHookDirectory(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	sourceDir := filepath.Join(repoDir, ".githooks")
	externalDir := t.TempDir()
	writeFileMode(t, filepath.Join(externalDir, "pre-commit"), "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.RemoveAll(sourceDir); err != nil {
		t.Fatalf("remove fixture hook directory: %v", err)
	}
	if err := os.Symlink(externalDir, sourceDir); err != nil {
		t.Fatalf("link fixture hook directory: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Missing or unsafe reviewed hook directory") {
		t.Fatalf("hooks-install with symlinked hook directory = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("symlinked source directory created managed hook state: %v", err)
	}
}

func TestHooksInstallSnapshotBackupIsAllocatedBeforeCopy(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	managedHook := managedHookPath(t, repoDir)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o777); err != nil {
		t.Fatalf("create writable managed directory: %v", err)
	}
	writeFileMode(t, managedHook, "#!/bin/sh\nexit 0\n", 0o755)
	marker := filepath.Join(t.TempDir(), "snapshot-path")
	wrapperDir := snapshotPathWrapper(t, marker, "*.pre-commit.backup.*")
	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("hooks-install = %v\n%s", err, output)
	}
	assertSnapshotPathObserved(t, marker)
	assertFileEquals(t, managedHook, readRepositoryHook(t))
}

func TestHooksInstallRejectsUnsafeAllocatedSnapshotAndCleansUp(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	victim := filepath.Join(t.TempDir(), "victim")
	writeFile(t, victim, "unchanged\n")
	mktempPath, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mktemp"), fmt.Sprintf(`#!/bin/sh
case "$1" in
*/.pre-commit.tmp.XXXXXX)
	path="${1%%XXXXXX}unsafe"
	ln -s %q "$path"
	printf '%%s\n' "$path"
	exit 0
	;;
esac
exec %q "$@"
`, victim, mktempPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to allocate safe hook installation snapshot") {
		t.Fatalf("hooks-install with unsafe snapshot = %v\n%s", err, output)
	}
	assertFileEquals(t, victim, "unchanged\n")
	if _, err := os.Stat(managedDir); !os.IsNotExist(err) {
		t.Fatalf("unsafe snapshot cleanup retained managed directory: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func assertIndependentHookFixture(t *testing.T, repoDir string) {
	t.Helper()
	commonDir, err := filepath.EvalSymlinks(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if err != nil {
		t.Fatalf("resolve fixture common directory: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatalf("resolve fixture .git directory: %v", err)
	}
	if commonDir != want {
		t.Fatalf("fixture git common directory = %q, want independent %q", commonDir, want)
	}
}

func assertSnapshotPathObserved(t *testing.T, marker string) {
	t.Helper()
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read snapshot path marker: %v", err)
	}
	if path := strings.TrimSpace(string(contents)); path == "" {
		t.Fatal("copy wrapper did not observe a snapshot path")
	}
}

func snapshotPathWrapper(t *testing.T, marker, pattern string) string {
	t.Helper()
	cpPath, err := exec.LookPath("cp")
	if err != nil {
		t.Fatalf("find cp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "cp"), fmt.Sprintf(`#!/bin/sh
for destination do :; done
case "$destination" in
%s) [ -f "$destination" ] && [ ! -L "$destination" ] || exit 74; printf '%%s\n' "$destination" > %q ;;
esac
exec %q "$@"
`, pattern, marker, cpPath), 0o755)
	return wrapperDir
}

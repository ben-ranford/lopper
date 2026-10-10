package gitexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandFilterProbeIsAnExactClosedProfile(t *testing.T) {
	repo := t.TempDir()
	probe := func(driver string) []string {
		args := append(SafeConfigArgs(), "-C", repo)
		return append(args, "-c", "filter."+driver+".clean=git hash-object --stdin --__LOPPER_LOCKFILE_FILTER_PROBE__",
			"-c", "filter."+driver+".process=git hash-object --stdin --__LOPPER_LOCKFILE_FILTER_PROBE__",
			"-c", "filter."+driver+".required=true", "hash-object", "--path=- odd;name\nfile", "--stdin")
	}
	for _, driver := range []string{"set", "unset", "unspecified"} {
		if _, err := Command(ExecutablePrimary, probe(driver)...); err != nil {
			t.Fatalf("valid %s probe: %v", driver, err)
		}
	}
	for _, driver := range []string{"other", "SET", "set.process=echo injected"} {
		assertRequestRejected(t, probe(driver))
	}
	for _, change := range []struct {
		offset int
		value  string
	}{
		{7, "filter.set.clean=touch /tmp/marker"}, {5, "filter.unset.process=git hash-object --stdin --__LOPPER_LOCKFILE_FILTER_PROBE__"},
		{3, "filter.set.required=false"}, {2, "status"}, {1, "--path="}, {1, "--path=a\x00b"}, {0, "--literally"},
	} {
		args := probe("set")
		args[len(args)-1-change.offset] = change.value
		assertRequestRejected(t, args)
	}
	assertRequestRejected(t, append(probe("set"), "extra"))
}

func TestCommandDashboardProfilesKeepURLsAndReferencesAsData(t *testing.T) {
	repo := t.TempDir()
	plain := []string{"-C", repo}
	for _, operation := range [][]string{
		{"init", filepath.Join(repo, "checkout")},
		{"clone", "--no-tags", "--depth=1", "--", "https://example.test/space%20name/repo.git", filepath.Join(repo, "clone")},
		{"clone", "--no-tags", "--depth=1", "--", "ssh://git@example.test/repo.git", filepath.Join(repo, "ssh")},
		{"-c", "protocol.file.allow=always", "clone", "--no-tags", "--depth=1", "--", "file:////fixture-host/share/repo", filepath.Join(repo, "file")},
	} {
		if _, err := Command(ExecutablePrimary, operation...); err != nil {
			t.Fatalf("%v: %v", operation, err)
		}
	}
	for _, operation := range [][]string{
		{"remote", "get-url", "origin"}, {"remote", "add", "origin", "file:////fixture-host/share/repo"},
		{"fetch", "--prune", "--depth=1", "origin", "HEAD"},
		{"fetch", "--prune", "--no-tags", "--depth=1", "origin", "refs/heads/feature-ä"},
		{"fetch", "--prune", "--no-tags", "--depth=1", "origin", "refs/tags/v1.0"},
		{"fetch", "--prune", "--no-tags", "--depth=1", "origin", strings.Repeat("B", 64)},
		{"checkout", "--detach", "--force", "FETCH_HEAD"}, {"reset", "--hard", "HEAD"}, {"clean", "-fdx"}, {"rev-parse", "--verify", "HEAD"},
	} {
		if _, err := Command(ExecutablePrimary, append(plain, operation...)...); err != nil {
			t.Fatalf("%v: %v", operation, err)
		}
	}
	for _, remote := range []string{"git://example.test/repo", "https://user:secret@example.test/repo", "ssh://git:secret@example.test/repo", "ssh://-user@example.test/repo", "file://foreign/tmp/repo", "file:///", "file://user@localhost/tmp/repo", "https://example.test/", "https://example.test/repo?query", "https://example.test/repo#fragment", "https://example.test/%00repo", "https://example.test:%zz/repo"} {
		assertRequestRejected(t, []string{"clone", "--no-tags", "--depth=1", "--", remote, repo})
	}
	for _, ref := range []string{"refs/heads/", "refs/heads/-option", "refs/heads/../main", "refs/heads/.hidden", "refs/heads/main.lock", "refs/heads/main.", "refs/heads/a//b", "refs/heads/with space", "refs/heads/a\x7fb", "refs/tags/a:b", "refs/heads/a@{0}", "HEAD", "refs/other/main"} {
		assertRequestRejected(t, append(plain, "fetch", "--prune", "--no-tags", "--depth=1", "origin", ref))
	}
}

func TestCommandRejectsMalformedGlobalsAndOperandShapes(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"-C"}, {"-c"}, {"-C", ""}, {"-C", "relative"}, {"-C", "a\x00b"},
		{"-c", "core.fsmonitor", "--version"}, {"-c", "core.fsmonitor=false", "-C", repo, "status", "--porcelain"},
		{"-c", "core.hooksPath=" + repo, "-C", repo, "rev-parse", "--verify", "HEAD"},
		{"-c", "protocol.file.allow=never", "-C", repo, "clean", "-fdx"},
		{"-c", "protocol.file.allow=always", "init", repo},
		{"-c", "protocol.file.allow=always", "clone", "--no-tags", "--depth=1", "--", "https://example.test/repo", repo},
		{"-c", "protocol.file.allow=always", "-C", repo, "rev-parse", "--is-inside-work-tree"},
		{"-C", repo, "remote", "get-url", "other"}, {"-C", repo, "remote", "add", "origin", "https://example.test/repo", "extra"},
		{"-C", repo, "diff", "--no-ext-diff"},
		{"-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z"},
		{"-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--", "--ext-diff"},
		{"-C", repo, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"},
		{"-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--", ":(literal)"},
		{"-C", repo, "ls-files", "--others", "--exclude-standard", "-z", "--", ":(literal)a\x00b"},
		{"-c", "core.hooksPath=" + os.DevNull, "-C", repo, "worktree", "add", "--detach", repo, "HEAD"},
		{"-c", "core.hooksPath=" + os.DevNull, "-C", repo, "worktree", "remove", "--force", "relative"},
		{"-c", "core.hooksPath=" + os.DevNull, "-C", repo, "worktree", "prune"},
	} {
		assertRequestRejected(t, args)
	}
	unsafe := append(SafeConfigArgs(), "-C", repo, "status", "--porcelain")
	unsafe[1] = "core.fsmonitor=untrusted"
	assertRequestRejected(t, unsafe)
}

func TestCommandRejectsInvalidProofRevisions(t *testing.T) {
	repo := t.TempDir()
	prefix := append(SafeConfigArgs(), "-c", "core.hooksPath="+os.DevNull, "-C", repo)
	for _, revision := range []string{"--option", "HEAD~n", "refs/heads/../main"} {
		assertRequestRejected(t, append(prefix, "merge-base", "--", revision, "HEAD"))
	}
	assertRequestRejected(t, append(prefix, "worktree", "add", "--detach", repo, strings.Repeat("g", 40)))
	assertRequestRejected(t, append(SafeConfigArgs(), "init", repo))
}

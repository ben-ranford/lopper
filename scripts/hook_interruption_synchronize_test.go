//go:build linux

package scripts

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSynchronizeBeforeFetchesOrUsesMergeBase covers a force-pushed synchronize
// predecessor: fetch that commit, otherwise diff the merge-base with the base
// branch, and fail closed when neither object is available.
func TestSynchronizeBeforeFetchesOrUsesMergeBase(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	script := filepath.Join(filepath.Dir(filename), "trace-hook-interruption.py")
	command := exec.Command(python, "-c", synchronizeBeforeProbe, script)
	command.Env = append(command.Environ(), "SYNC_GIT="+git)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("synchronize predecessor contract: %v\n%s", err, output)
	}
}

const synchronizeBeforeProbe = `
import os
import pathlib
import runpy
import subprocess
import tempfile

api = runpy.run_path(__import__("sys").argv[1], run_name="synchronize_probe")
binding = api["synchronize_binding"]
missing = "0123456789abcdef0123456789abcdef01234567"
absent_base = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
git_bin = os.environ["SYNC_GIT"]

def g(repo, *args, output=False):
    result = subprocess.run([git_bin, "-c", "protocol.file.allow=always", "-c", "user.email=trace@example.com",
                             "-c", "user.name=trace", "-C", str(repo), *args], check=True,
                            stdout=subprocess.PIPE if output else subprocess.DEVNULL, text=True)
    return result.stdout.strip() if output else None

def event(before, head, base, ref="main"):
    return {"action": "synchronize", "before": before, "after": head,
            "pull_request": {"base": {"sha": base, "ref": ref}}}

with tempfile.TemporaryDirectory() as directory:
    directory = pathlib.Path(directory)
    seed, origin, work = directory / "seed", directory / "origin.git", directory / "work"
    seed.mkdir()
    subprocess.run([git_bin, "init", "-q", "-b", "main", str(seed)], check=True)
    source = seed / "scripts" / "trace-hook-interruption.py"
    source.parent.mkdir(parents=True)
    source.write_text("base\n")
    g(seed, "add", "scripts/trace-hook-interruption.py")
    g(seed, "commit", "-q", "-m", "base")
    base = g(seed, "rev-parse", "HEAD", output=True)
    source.write_text("same\n")
    g(seed, "commit", "-q", "-am", "before")
    before = g(seed, "rev-parse", "HEAD", output=True)
    g(seed, "reset", "-q", "--hard", base)
    source.write_text("same\n")
    g(seed, "commit", "-q", "-am", "head")
    head = g(seed, "rev-parse", "HEAD", output=True)
    subprocess.run([git_bin, "init", "-q", "--bare", "-b", "main", str(origin)], check=True)
    g(origin, "config", "uploadpack.allowReachableSHA1InWant", "true")
    g(origin, "config", "uploadpack.allowAnySHA1InWant", "true")
    g(seed, "remote", "add", "origin", str(origin))
    g(seed, "push", "-q", "origin", "HEAD:main")
    g(seed, "push", "-q", "origin", before + ":refs/heads/replaced-tip")
    subprocess.run([git_bin, "-c", "protocol.file.allow=always", "clone", "-q", "--no-local", "--single-branch",
                    "--branch", "main", str(origin), str(work)], check=True)
    g(work, "config", "protocol.file.allow", "always")
    present = subprocess.run([git_bin, "-C", str(work), "cat-file", "-e", before + "^{commit}"],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if present.returncode == 0:
        raise SystemExit("single-branch clone contained the replaced tip")
    if binding(work, event(before, head, base), head) is not False:
        raise SystemExit("fetched predecessor did not bound an empty diagnostic diff")
    if binding(work, event(missing, head, base), head) is not True:
        raise SystemExit("merge-base fallback missed a diagnostic source change")
    g(work, "update-ref", "refs/remotes/origin/main", base)
    if binding(work, event(missing, head, absent_base), head) is not True:
        raise SystemExit("base branch ref did not provide a merge-base")
    try:
        binding(work, event(missing, head, absent_base, "no-such-base"), head)
    except ValueError as error:
        if str(error) != "unreachable synchronize predecessor":
            raise SystemExit("fail-closed error was " + str(error))
    else:
        raise SystemExit("unbounded synchronize predecessor accepted")
    other = work / "other.txt"
    other.write_text("other\n")
    g(work, "add", "other.txt")
    g(work, "commit", "-q", "-m", "other")
    head_same = g(work, "rev-parse", "HEAD", output=True)
    if binding(work, event(head, head_same, base), head_same) is not False:
        raise SystemExit("local predecessor lost to the base merge-base")
`

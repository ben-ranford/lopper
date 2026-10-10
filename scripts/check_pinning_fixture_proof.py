#!/usr/bin/env python3
"""Fixed, unprivileged Linux evidence for the reviewed pinning fixture repair."""

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import time

GOENV_KEY = 'GOENV'
TOOLCHAIN_KEY = 'GOTOOLCHAIN'
CGO_KEY = 'CGO_ENABLED'
STATUS_KEY = 'status'
JOINED_KEY = 'joined'
ERROR_KEY = 'error'
PYTHON_TOOL = 'python3'
VERSION_FLAG = '--version'
STDOUT_SUFFIX = '.stdout'

ORIGINAL = "f0da6e1200f4e9f190932a2cb241d8c4ae6f21d8"
REPAIR = "6ce1817515bd9d84437804925407493d1b6f0e12"
SUBJECT = "scripts/automation_integrity_head_test.go"
SUBJECT_SHA = "c98ba1b32aae742bd4f7c3ec19873dc08deb6ba4c0e2eb16badbe937ad9db146"
BASE_SHA = "b7b3f0d4c0a823c26835ee1bcc533151d6c32ec2885c3ab0e49bcd978211d698"
INVERSE_SHA = "90797583198f359aaabf920b0f0b3359713709bf692e37ed20c4514b6ad6d8a1"
SUBJECTS = {
    SUBJECT: SUBJECT_SHA,
    "scripts/check-github-actions-pinning.sh": "2ef9261bbbe92eb0f95a043689d70eb16d36c9b698e44e052b30a05cc4612969",
    "go.mod": "de58e60e54a1c52045a40b6e5086db6fad6c5fffc4f3a69295fac6f395b69111",
    "go.sum": "243ba6e05faeb2c4328d1d550617c77f0c343a6f276d1b64848b143ac47b954d",
}
TESTS = (
    "TestGitHubActionsPinningRejectsMutableActionRef",
    "TestGitHubActionsPinningRejectsMutableCompositeActionRef",
    "TestGitHubActionsPinningSupportsAnchoredYamlWorkflow",
)
LAUNCH = b'exec.Command("sh", filepath.Join(repoDir, "scripts", "check-github-actions-pinning.sh"))'
COMMAND_CAP = 16 * 1024 * 1024
EVIDENCE_CAP = 128 * 1024 * 1024
METADATA_RESERVE = 1024 * 1024
TOTAL_SECONDS = 40 * 60
CLEANUP_SECONDS = 30
COMMAND_SECONDS = 15 * 60


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    with Path(path).open("rb") as stream:
        value = hashlib.sha256()
        for chunk in iter(lambda: stream.read(65536), b""):
            value.update(chunk)
    return value.hexdigest()


def write_json(path, value):
    data = (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()
    require(len(data) <= METADATA_RESERVE, "metadata receipt too large")
    path = Path(path)
    metadata = sum(p.stat().st_size for p in path.parent.iterdir()
                   if p != path and p.is_file() and p.suffix not in (STDOUT_SUFFIX, ".stderr"))
    require(metadata + len(data) <= METADATA_RESERVE, "aggregate metadata cap exceeded")
    path.write_bytes(data)


def binding(path):
    path = Path(path)
    return {"path": str(path), "sha256": digest(path), "bytes": path.stat().st_size,
            "mode": stat.S_IMODE(path.stat().st_mode)}


def private_directory(path):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path and not path.is_symlink(),
            "noncanonical private directory")
    require(path.is_dir() and path.stat().st_uid == os.getuid()
            and stat.S_IMODE(path.stat().st_mode) == 0o700,
            "private directory mode")
    return path


def group_exists(pid):
    try:
        os.killpg(pid, 0)
        return True
    except ProcessLookupError:
        return False


def signal_group(pid, number):
    try:
        os.killpg(pid, number)
    except ProcessLookupError:
        pass


class Commands:
    """One invocation's joined processes, output accounting and fixed deadline."""

    def __init__(self, receipts, environment):
        self.receipts = receipts
        self.environment = environment
        self.deadline = time.monotonic() + TOTAL_SECONDS
        self.used = 0
        self.records = []
        self.failed = False

    def consume(self, selector, output, allowance):
        for key, _ in selector.select(timeout=min(0.05, max(0, allowance))):
            chunk = os.read(key.fileobj.fileno(), 65536)
            if not chunk:
                selector.unregister(key.fileobj)
                key.fileobj.close()
                continue
            remaining = min(COMMAND_CAP - output["read"],
                            EVIDENCE_CAP - METADATA_RESERVE - self.used)
            output["read"] += len(chunk)
            admitted = chunk[:max(0, remaining)]
            count = key.data.write(admitted)
            require(count == len(admitted), "short output write")
            self.used += count
            output["written"] += count
            require(len(admitted) == len(chunk), "command/evidence output cap exceeded")

    def drain_until(self, child, selector, output, deadline):
        while selector.get_map() or child.poll() is None:
            require(time.monotonic() < deadline, "command deadline or EOF drain expired")
            self.consume(selector, output, deadline - time.monotonic())
        require(child.wait(timeout=max(0.001, deadline - time.monotonic())) is not None,
                "leader was not reaped")
        require(not group_exists(child.pid), "descendant group still alive after leader/EOF")

    def cleanup(self, child, selector, output):
        # The normal allowance ends 30 seconds before the absolute total deadline.
        end = min(self.deadline, time.monotonic() + CLEANUP_SECONDS)
        signal_group(child.pid, signal.SIGTERM)
        term_end = min(end, time.monotonic() + 10)
        while child.poll() is None and time.monotonic() < term_end:
            time.sleep(min(0.05, max(0, term_end - time.monotonic())))
        signal_group(child.pid, signal.SIGKILL)
        try:
            child.wait(timeout=max(0.001, min(10, end - time.monotonic())))
        except subprocess.TimeoutExpired:
            return False
        # A failed output sink stays disabled; drain only for bounded custody.
        while selector.get_map() and time.monotonic() < end:
            for key, _ in selector.select(timeout=0.05):
                chunk = os.read(key.fileobj.fileno(), 65536)
                output["discarded_cleanup"] = output.get("discarded_cleanup", 0) + len(chunk)
                if not chunk:
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
        return not selector.get_map() and not group_exists(child.pid)

    def run(self, name, argv, cwd):
        require(not self.failed, "no launch after an earlier command failure")
        end = min(self.deadline - CLEANUP_SECONDS, time.monotonic() + COMMAND_SECONDS)
        require(end > time.monotonic(), "total command allowance exhausted")
        record = {"name": name, "argv": [str(v) for v in argv], "cwd": str(cwd),
                  "started": time.time(), "environment": self.environment,
                  "exit": None, JOINED_KEY: False, ERROR_KEY: None}
        selector = selectors.DefaultSelector()
        child = None
        output = {"read": 0, "written": 0}
        sinks = []
        try:
            for channel in ("stdout", "stderr"):
                path = self.receipts / (name + "." + channel)
                sinks.append(path.open("xb"))
            require(time.monotonic() < end, "launch allowance exhausted")
            child = subprocess.Popen(record["argv"], cwd=cwd, env=self.environment,
                                     stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                     start_new_session=True)
            record["pid"] = child.pid
            for pipe, sink in zip((child.stdout, child.stderr), sinks):
                os.set_blocking(pipe.fileno(), False)
                selector.register(pipe, selectors.EVENT_READ, sink)
            self.drain_until(child, selector, output, end)
            record.update(exit=child.returncode, joined=True)
        except BaseException as error:
            self.failed = True
            record[ERROR_KEY] = str(error)
            if child is not None:
                record[JOINED_KEY] = self.cleanup(child, selector, output)
                record["exit"] = child.returncode
            raise
        finally:
            for key in selector.get_map().values():
                key.fileobj.close()
            selector.close()
            close_errors = []
            for sink in sinks:
                try:
                    sink.close()
                except OSError as error:
                    close_errors.append(str(error))
            if close_errors:
                self.failed = True
                record["finalization_errors"] = close_errors
                if record[ERROR_KEY] is None:
                    record[ERROR_KEY] = "output finalization: " + "; ".join(close_errors)
            record.update(ended=time.time(), output=output)
            self.records.append(record)
            write_json(self.receipts / (name + ".json"), record)
        require(record[ERROR_KEY] is None, "output finalization failed")
        return record

    def success(self, name, argv, cwd):
        record = self.run(name, argv, cwd)
        if record["exit"] != 0:
            self.failed = True
            raise RuntimeError(name + " failed; not a behavioral red")
        return (self.receipts / (name + STDOUT_SUFFIX)).read_bytes()


def test_result(record, stdout, stderr, test, red):
    require(record[JOINED_KEY] and record[ERROR_KEY] is None, "test process custody failed")
    text = (stdout + stderr).decode("utf-8", errors="strict")
    runs = re.findall(r"^=== RUN   (\S+)\s*$", text, re.MULTILINE)
    results = re.findall(r"^--- (PASS|FAIL|SKIP): (\S+) \([^\n]+\)$", text, re.MULTILINE)
    require(runs == [test], "missing, duplicate or wrong RUN frame")
    require(results == [("FAIL" if red else "PASS", test)], "wrong named result or skip")
    require(record["exit"] == (1 if red else 0), "whole TestMain exit disagrees")
    require(re.findall(r"^(PASS|FAIL)$", text, re.MULTILINE) == ["FAIL" if red else "PASS"],
            "wrong whole-package test result")
    cleanup_errors = ("create benchmark fixture Go cache:", "remove benchmark fixture Go cache:",
                      "close pinning fixture writer:", "TempDir RemoveAll cleanup:")
    require(not any(message in text for message in cleanup_errors), "setup/cleanup is not expected test behavior")
    if not red:
        return
    require("panic:" not in text and "test timed out" not in text, "panic is not expected red")
    if test == TESTS[2]:
        require("expected anchored .yaml workflow to pass, got fork/exec " in text
                and "text file busy" in text, "missing anchored ETXTBSY evidence")
    else:
        require('output missing "GitHub Actions pinning check failed"' in text,
                "missing original negative-policy-output assertion")


def inventory(root):
    rows = {}
    for path in sorted(root.rglob("*")):
        require(not path.is_symlink(), "unexpected symlink in archived subject")
        if path.is_file():
            rows[str(path.relative_to(root))] = [digest(path), stat.S_IMODE(path.stat().st_mode)]
    return rows


def verify_subject(root):
    for relative, expected in SUBJECTS.items():
        require(digest(root / relative) == expected, "subject changed: " + relative)


def inverse(data):
    require(hashlib.sha256(data).hexdigest() == SUBJECT_SHA and data.count(LAUNCH) == 3,
            "repair subject or launch count changed")
    result = data.replace(LAUNCH, LAUNCH.replace(b'"sh", ', b""))
    require(hashlib.sha256(result).hexdigest() == INVERSE_SHA, "inverse bytes changed")
    return result


def archive(commands, git, repository, revision, destination, scratch):
    path = scratch / (destination.name + ".tar")
    commands.success("archive-" + destination.name,
                     [git, "-c", "tar.umask=0022", "archive", "--format=tar", "--output=" + str(path), revision], repository)
    destination.mkdir(mode=0o700)
    with tarfile.open(path) as source:
        for member in source:
            require(time.monotonic() < commands.deadline - CLEANUP_SECONDS, "archive deadline expired")
            target = destination / member.name
            require(not Path(member.name).is_absolute() and ".." not in Path(member.name).parts,
                    "archive path escaped")
            require(member.isdir() or member.isfile(), "archive special entry")
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.extractfile(member) as src, target.open("xb") as dst:
                    shutil.copyfileobj(src, dst, 65536)
                target.chmod(member.mode)
    return inventory(destination)


def resolve_tool(name):
    selected = shutil.which(name)
    require(selected is not None, "missing required tool: " + name)
    resolved = Path(selected).resolve(strict=True)
    require(resolved.is_file() and os.access(resolved, os.X_OK), "tool is not executable")
    return resolved


def tool_search_path(tools):
    selected = {str(Path(shutil.which(name)).parent) for name in tools if name != "sh"}
    selected.add("/bin")
    directories = dict.fromkeys(part for part in os.environ["PATH"].split(os.pathsep) if part in selected)
    if "/bin" not in directories:
        directories["/bin"] = None
    path = os.pathsep.join(directories)
    for name, expected in tools.items():
        actual = shutil.which(name, path=path)
        require(actual is not None and Path(actual).resolve() == expected, "private PATH changes tool: " + name)
    return path


def environment(root, tools):
    for name in ("GOCACHEPROG", "GOFLAGS", GOENV_KEY, TOOLCHAIN_KEY):
        require(not os.environ.get(name), "ambient Go override rejected: " + name)
    env = {"PATH": tool_search_path(tools),
           "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "GOMAXPROCS": "2",
           TOOLCHAIN_KEY: "local", GOENV_KEY: "off", CGO_KEY: "1",
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
           "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0",
           "CC": str(tools["cc"])}
    for name in ("HOME", "TMPDIR", "GOCACHE", "GOMODCACHE"):
        path = root / name.lower()
        path.mkdir(mode=0o700)
        env[name] = str(path)
    return env


def qualify(commands, tools, scratch):
    require(sys.platform == "linux" and platform.machine() == "x86_64", "native Linux/amd64 required")
    require(os.environ.get("RUNNER_OS") == "Linux" and os.environ.get("RUNNER_ARCH") == "X64"
            and os.environ.get("ImageOS") and os.environ.get("ImageVersion"), "missing native runner image identity")
    versions = {}
    for name, arguments in {"go": ["version"], "git": [VERSION_FLAG], PYTHON_TOOL: [VERSION_FLAG],
                            "ruby": [VERSION_FLAG], "cc": [VERSION_FLAG]}.items():
        versions[name] = commands.success("version-" + name, [tools[name], *arguments], scratch).decode()
    require(versions["go"].strip() == "go version go1.27.2 linux/amd64", "unexpected Go compiler")
    commands.success("ruby-yaml", [tools["ruby"], "-ryaml", "-e", "abort unless YAML.safe_load('a: 1')['a'] == 1"], scratch)
    commands.success("shell", [tools["sh"], "-c", "test -n \"$0\""], scratch)
    probe = scratch / "executable-probe.sh"
    probe.write_text("#!/bin/sh\nprintf 'executable-ok\\n'\n")
    probe.chmod(0o700)
    require(commands.success("executable-filesystem", [probe], scratch) == b"executable-ok\n",
            "ordinary executable filesystem probe failed")
    env = json.loads(commands.success("go-env", [tools["go"], "env", "-json", "GOVERSION", "GOOS", "GOARCH", "GOROOT", "GOTOOLDIR", CGO_KEY, TOOLCHAIN_KEY, GOENV_KEY, "GOMODCACHE", "GOCACHE", "GOPROXY", "GOSUMDB"], scratch))
    require(env["GOVERSION"] == "go1.27.2" and env["GOOS"] == "linux" and env["GOARCH"] == "amd64"
            and env[CGO_KEY] == "1" and env[TOOLCHAIN_KEY] == "local", "Go build context mismatch")
    require(env["GOPROXY"] == "https://proxy.golang.org,direct" and env["GOSUMDB"] == "sum.golang.org",
            "module authentication policy changed")
    for name in ("compile", "link"):
        tools[name] = Path(env["GOTOOLDIR"]) / name
    return {"versions": versions, "go_env": env, "uname": list(os.uname()), "uid": os.getuid(),
            "image": {key: os.environ.get(key) for key in ("ImageOS", "ImageVersion", "RUNNER_OS", "RUNNER_ARCH")},
            "os_release": Path("/etc/os-release").read_text(),
            "mountinfo": Path("/proc/self/mountinfo").read_text(), "scratch_device": scratch.stat().st_dev,
            "tools": {name: binding(path) for name, path in tools.items()}}


def compile_variant(commands, tools, root, output, name, race=False):
    args = [tools["go"], "test", "-mod=readonly", "-p=2"]
    if race:
        args.append("-race")
    args += ["-c", "-o", output, "./scripts"]
    commands.success(name + "-compile", args, root)
    info = commands.success(name + "-buildinfo", [tools["go"], "version", "-m", output], root).decode()
    require(info.splitlines()[0] == str(output) + ": go1.27.2", "binary compiler identity mismatch")
    require(("\tbuild\t-race=true\n" in info) == race, "binary race mode mismatch")
    for setting in ("GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1", "-compiler=gc"):
        require("\tbuild\t" + setting + "\n" in info, "missing binary setting: " + setting)
    write_json(commands.receipts / (name + "-binary.json"), binding(output))


def run_tests(commands, root, binary, name, red):
    for number, test in enumerate(TESTS):
        label = name + "-" + str(number)
        before = binding(binary)
        record = commands.run(label, [binary, "-test.v", "-test.count=1", "-test.timeout=15m",
                                      "-test.run=^" + test + "$"], root / "scripts")
        test_result(record, (commands.receipts / (label + STDOUT_SUFFIX)).read_bytes(),
                    (commands.receipts / (label + ".stderr")).read_bytes(), test, red)
        require(binding(binary) == before, "test binary changed during execution")


def prove(commands, tools, repository, scratch):
    original = scratch / "F"
    candidate = scratch / "C"
    old = archive(commands, tools["git"], repository, ORIGINAL, original, scratch)
    expected = archive(commands, tools["git"], repository, REPAIR, candidate, scratch)
    require(set(old) == set(expected), "repair changed source path set")
    require([p for p in old if old[p] != expected[p]] == [SUBJECT], "repair is not the one-file delta")
    require(digest(original / SUBJECT) == BASE_SHA, "original source identity changed")
    verify_subject(candidate)
    baseline = scratch / "B"
    mutation = scratch / "I"
    for target in (baseline, mutation):
        shutil.copytree(candidate, target)
        (target / SUBJECT).write_bytes(inverse((candidate / SUBJECT).read_bytes()))
    baseline_inventory = inventory(baseline)
    require(baseline_inventory == inventory(mutation), "inverse is not byte-identical to instrumented parent")
    (commands.receipts / "original-subject.go.txt").write_bytes((original / SUBJECT).read_bytes())
    write_json(commands.receipts / "variant-inventories.json", {"F": old, "C": expected, "B_equals_I": baseline_inventory})
    commands.success("modules-download", [tools["go"], "mod", "download", "-json"], candidate)
    commands.success("modules-verify", [tools["go"], "mod", "verify"], candidate)
    verify_subject(candidate)
    for name, root, red, race in (("B", baseline, True, False), ("C", candidate, False, False),
                                  ("C-race", candidate, False, True), ("I", mutation, True, False)):
        binary = scratch / (name + ".test")
        compile_variant(commands, tools, root, binary, name, race)
        run_tests(commands, root, binary, name, red)
    (mutation / SUBJECT).write_bytes((candidate / SUBJECT).read_bytes())
    restored = scratch / "restored.test"
    compile_variant(commands, tools, mutation, restored, "restored")
    run_tests(commands, mutation, restored, "restored", False)
    require(inventory(candidate) == expected and inventory(mutation) == expected
            and inventory(baseline) == baseline_inventory and inventory(original) == old,
            "variant source/modes changed after consumption")
    commands.success("modules-verify-after", [tools["go"], "mod", "verify"], candidate)


def main():
    root = private_directory(Path(os.environ["PINNING_PROOF_ROOT"]))
    runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    require(root.parent == runner_temp and root.name.startswith("pinning-proof."), "root is outside invocation namespace")
    receipts = private_directory(root / "receipts")
    result = {STATUS_KEY: "INCOMPLETE", "native_windows": "UNRUN", "external_review_binding": "REQUIRED"}
    commands = None
    try:
        bootstrap = json.loads((receipts / "bootstrap.json").read_text())
        require(bootstrap[STATUS_KEY] == "ADMITTED" and bootstrap["O"] == REPAIR, "missing bootstrap admission")
        repository = Path(os.environ["GITHUB_WORKSPACE"]).resolve(strict=True)
        require(repository not in root.parents and root not in repository.parents and root != repository,
                "proof scratch overlaps checkout")
        verify_subject(repository)
        scratch = root / "work"
        scratch.mkdir(mode=0o700)
        tools = {name: resolve_tool(name) for name in ("go", "git", PYTHON_TOOL, "ruby", "cc")}
        tools["sh"] = Path("/bin/sh").resolve(strict=True)
        require(all(repository not in tool.parents and root not in tool.parents for tool in tools.values()),
                "runtime tool is inside subject/scratch")
        require(Path(sys.executable).resolve() == tools[PYTHON_TOOL], "driver interpreter differs from qualified Python")
        driver = Path(__file__).resolve()
        require(driver == repository / "scripts/check_pinning_fixture_proof.py", "wrong driver location")
        require(digest(driver) == bootstrap["driver_sha256"], "driver differs from admitted W")
        commands = Commands(receipts, environment(scratch, tools))
        runtime = qualify(commands, tools, scratch)
        write_json(receipts / "runtime-before.json", runtime)
        prove(commands, tools, repository, scratch)
        require({name: binding(path) for name, path in tools.items()} == runtime["tools"],
                "tool bytes changed after consumption")
        write_json(receipts / "runtime-after.json", {name: binding(path) for name, path in tools.items()})
        verify_subject(repository)
        require(digest(driver) == bootstrap["driver_sha256"]
                and digest(repository / ".github/workflows/pinning-fixture-proof.yml") == bootstrap["workflow_sha256"],
                "W source changed after consumption")
        require(time.monotonic() < commands.deadline - CLEANUP_SECONDS, "total proof allowance exhausted")
        require(sum(p.stat().st_size for p in receipts.iterdir() if p.is_file()) <= EVIDENCE_CAP - 4096,
                "final evidence cap exceeded")
        result[STATUS_KEY] = "NATIVE_PROOF_COMPLETE_PENDING_EXTERNAL_BINDING"
    except BaseException as error:
        result[ERROR_KEY] = str(error)
    finally:
        result["commands"] = len(commands.records) if commands else 0
        result["finished"] = time.time()
        write_json(receipts / "result.json", result)
    return 0 if result[STATUS_KEY] == "NATIVE_PROOF_COMPLETE_PENDING_EXTERNAL_BINDING" else 1


if __name__ == "__main__":
    sys.exit(main())

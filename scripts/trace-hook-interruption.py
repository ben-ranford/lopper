#!/usr/bin/python3
"""Fixed normal-mode hook diagnostic. Consistency checks are not review authority."""
import hashlib
import json
import os
import pathlib
import platform
import re
import shlex
import shutil
import stat
import subprocess
import sys
import time

TRACE_SUFFIX = '.trace'
RECEIPT_SUFFIX = '.receipt.json'
MAIN_NAME = 'main.json'
MANIFEST_NAME = 'manifest.json'
LOG_NAME = 'preflight.log'
PYTHON = '/usr/bin/python3'
STRACE = '/usr/bin/strace'
TREE_SUFFIX = '^{tree}'
DIRECTORY_ERROR = 'sink directory custody'

SOURCE_PATHS = (".github/workflows/ci-tests.yml", "scripts/trace-hook-interruption.py",
                "scripts/testdata/hook-interruption-trace-controls.py")
HOOK_SOURCES = ("scripts/pre_commit_hook_preflight_timeout_test.go", "scripts/pre_commit_hook_test.go",
                "scripts/hook-config-preflight.sh", "Makefile")
# Exact hosted tracers. ubuntu-latest is mid-migration from 24.04 to 26.04.
REVIEWED_TRACERS = (
    ("28f957c227012de0b18d1bd7fff2d396cb693ea60ed8013be68de071e84b5001", b"version 6.8", b"6.8-0ubuntu2 amd64"),
    ("66e7dbc556b949ba29efc6b02948580857b9a9c99adacf949cdc10c57d930fc5", b"version 6.19", b"6.19+ds-0ubuntu5 amd64"),
)
IMAGE_VERSION = re.compile(r"\d{8}\.\d+\.\d+", re.ASCII)
COMMIT_SHA = re.compile(r"[0-9a-f]{40}")
SYSCALLS = ("clone,clone3,fork,vfork,execve,execveat,exit,exit_group,wait4,waitid,"
            "setpgid,getpgid,getpgrp,setsid,getsid,kill,tkill,tgkill,rt_sigaction,"
            "rt_sigprocmask,rt_sigreturn,pipe,pipe2,dup,dup2,dup3,fcntl,close,"
            "close_range,open,openat,openat2,read,readv,write,writev")
SELECTOR = "^TestHooksInstallInterruptCleansPreflightAndRollsBackState$"
SHOW_PARENTS = "--format=%P"
MERGE_PARENTS = "checkout not actual base/head merge"
CANDIDATE_TREE = "merge contains source outside the exact candidate tree"
UNREACHABLE_PREDECESSOR = "unreachable synchronize predecessor"
TRACE_NAMES = {
    "exit0": "exit0.trace",
    "exit23": "exit23.trace",
    "signal": "signal.trace",
    "groups": "groups.trace",
    "cap": "cap.trace",
    "write-error": "write-error.trace",
    "missing": "missing.trace",
    "abnormal": "abnormal.trace",
    "cancel": "cancel.trace",
    "focused": "focused.trace",
}
STREAMS = tuple(TRACE_NAMES)
CAPS = {name: (8388608 if name in ("cap", "focused") else 65536) for name in STREAMS}
SLOTS = {TRACE_NAMES[name]: CAPS[name] for name in STREAMS}
SLOTS.update({name + RECEIPT_SUFFIX: 4096 for name in STREAMS})
SLOTS.update({MAIN_NAME: 32768, MANIFEST_NAME: 32768, LOG_NAME: 65536})
MAX_RETAINED = 17473536
COUNTER_MAX = (1 << 63) - 1


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_identity(path):
    path = pathlib.Path(path)
    actual = path.resolve(strict=True)
    info = actual.stat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("identity is not a regular file")
    h = hashlib.sha256()
    with actual.open("rb") as stream:
        for chunk in iter(lambda: stream.read(65536), b""):
            h.update(chunk)
    return {"realpath": str(actual), "uid": info.st_uid, "mode": stat.S_IMODE(info.st_mode),
            "bytes": info.st_size, "sha256": h.hexdigest()}


def bounded_read(path, limit):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("nonregular or oversized record")
        data = bytearray()
        while len(data) <= limit:
            chunk = os.read(fd, min(4096, limit + 1 - len(data)))
            if not chunk:
                return bytes(data)
            data.extend(chunk)
        raise ValueError("oversized record")
    finally:
        os.close(fd)


def owned_directory(directory):
    """Admit only this run's generated private child, then retain directory custody."""
    temp = pathlib.Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    identifiers = [os.environ[name] for name in ("GITHUB_RUN_ID", "GITHUB_JOB", "GITHUB_RUN_ATTEMPT")]
    if any(not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value, re.ASCII) for value in identifiers):
        raise ValueError(DIRECTORY_ERROR)
    name = "hook-native-" + "-".join(identifiers)
    if os.fspath(directory) != str(temp / name):
        raise ValueError(DIRECTORY_ERROR)
    expected_parent = temp.stat()
    parent = os.open(temp, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    descriptor = None
    try:
        parent_info = os.fstat(parent)
        if (parent_info.st_dev, parent_info.st_ino) != (expected_parent.st_dev, expected_parent.st_ino):
            raise ValueError(DIRECTORY_ERROR)
        private_parent = parent_info.st_uid == os.getuid() and not parent_info.st_mode & 0o022
        sticky_temp = parent_info.st_uid == 0 and stat.S_IMODE(parent_info.st_mode) == 0o1777
        if not (private_parent or sticky_temp):
            raise ValueError(DIRECTORY_ERROR)
        expected = os.stat(name, dir_fd=parent, follow_symlinks=False)
        if not stat.S_ISDIR(expected.st_mode) or expected.st_uid != os.getuid() or stat.S_IMODE(expected.st_mode) != 0o700:
            raise ValueError(DIRECTORY_ERROR)
        descriptor = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        actual = os.fstat(descriptor)
        current = os.stat(name, dir_fd=parent, follow_symlinks=False)
        if (actual.st_dev, actual.st_ino, actual.st_uid, actual.st_mode) != (expected.st_dev, expected.st_ino, expected.st_uid, expected.st_mode) or (current.st_dev, current.st_ino) != (actual.st_dev, actual.st_ino):
            raise ValueError(DIRECTORY_ERROR)
        result = descriptor
        descriptor = None
        return result
    finally:
        if descriptor is not None:
            os.close(descriptor)
        os.close(parent)


def exclusive_write(directory, name, data):
    """One allocation: exclusive temp, checked writes/close, then rename; retain failures."""
    if name not in SLOTS or len(data) > SLOTS[name]:
        raise ValueError("slot or metadata bound")
    owned = not isinstance(directory, int)
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW) if owned else directory
    try:
        try:
            os.stat(name, dir_fd=descriptor, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            raise FileExistsError("final slot already exists")
        temporary = name + ".tmp"
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        try:
            view = memoryview(data)
            while view:
                wrote = os.write(fd, view)
                if wrote <= 0:
                    raise OSError(ZERO_WRITE)
                view = view[wrote:]
            os.fsync(fd)
        finally:
            os.close(fd)
        os.rename(temporary, name, src_dir_fd=descriptor, dst_dir_fd=descriptor)
    finally:
        if owned:
            os.close(descriptor)


def publish(directory, name, value):
    exclusive_write(directory, name, json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n")


def write_chunk(target, chunk, count):
    offset = 0
    try:
        while offset < count:
            wrote = os.write(target, chunk[offset:count])
            if wrote <= 0:
                raise OSError(ZERO_WRITE)
            offset += wrote
    except OSError:
        return offset, True
    return offset, False


def drain(source, target, cap):
    """4KiB allocation, capped retention, continued EOF drain on cap/write failure."""
    received = retained = discarded = 0
    errors = []
    eof = saturated = False
    while True:
        try:
            chunk = os.read(source, 4096)
        except OSError:
            errors.append("READ_ERROR")
            break
        if not chunk:
            eof = True
            break
        total = received + len(chunk)
        saturated = saturated or total > COUNTER_MAX
        received = min(total, COUNTER_MAX)
        count = min(len(chunk), cap - retained) if not errors else 0
        offset, write_error = write_chunk(target, chunk, count)
        retained += offset
        if write_error:
            errors.append("WRITE_ERROR")
        discarded = min(COUNTER_MAX, discarded + len(chunk) - offset)
    return {"received": received, "retained": retained, "discarded": discarded,
            "counter_saturated": saturated, "eof": eof, "errors": errors}


def sink_publication(directory, slot, receipt, witness_fd, release_fd):
    state = "COMPLETE"
    if receipt["errors"]:
        state = "FAILED_IO"
    elif receipt["discarded"] or receipt["counter_saturated"]:
        state = "INCOMPLETE"
    receipt.update({"version": 1, "slot": slot, "driver_sha256": file_identity(__file__)["sha256"],
                    "state": state})
    if slot == "missing":
        return 17
    publish(directory, slot + RECEIPT_SUFFIX, receipt)
    if slot == "abnormal":
        if witness_fd is None or release_fd is None:
            raise ValueError("private abnormal witness absent")
        os.write(witness_fd, (str(os.getpid()) + "\n").encode())
        # Private owner witnesses this live PID with a pidfd, then performs its
        # fixed SIGKILL. A completion receipt still cannot reveal waited status.
        os.read(release_fd, 1)
        raise ValueError("private owner did not perform witnessed termination")
    return 0 if receipt["state"] == "COMPLETE" else 1



def checked_sink_close(fd, output):
    errors = []
    if output != fd:
        try:
            os.close(output)
        except OSError:
            errors.append("CLOSE_ERROR")
    try:
        os.close(fd)
    except OSError:
        errors.append("CLOSE_ERROR")
    return errors


def sink(directory, slot, witness_fd=None, release_fd=None):
    if slot not in TRACE_NAMES:
        raise ValueError("invalid fixed stream")
    trace_name = TRACE_NAMES[slot]
    descriptor = owned_directory(directory)
    try:
        fd = os.open(trace_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        output = fd
        errors = []
        try:
            # Only the fixed private write-error slot exercises /dev/full. Never focused.
            if slot == "write-error":
                output = os.open("/dev/full", os.O_WRONLY)
            receipt = drain(0, output, CAPS[slot])
            try:
                os.fsync(fd)
            except OSError:
                errors.append("FLUSH_ERROR")
        finally:
            errors.extend(checked_sink_close(fd, output))
        receipt["errors"].extend(errors)
        return sink_publication(descriptor, slot, receipt, witness_fd, release_fd)
    finally:
        os.close(descriptor)


def trace_command(driver, directory, slot, target, witness_fd=None, release_fd=None):
    command = [PYTHON, "-I", "-S", str(driver), "sink", str(directory), slot]
    if witness_fd is not None:
        if slot != "abnormal":
            raise ValueError("private seam on wrong slot")
        command.extend([str(witness_fd), str(release_fd)])
    return [STRACE, "-f", "-I", "2", "-ttt", "-T", "-e", "raw=all",
            "-e", "trace=" + SYSCALLS, "-e", "signal=all", "-o", "|" + shlex.join(command), *target]


def receipt_shape(receipt, slot):
    keys = {"version", "slot", "driver_sha256", "state", "received", "retained", "discarded",
            "counter_saturated", "eof", "errors"}
    if set(receipt) != keys or type(receipt["version"]) is not int or receipt["version"] != 1 or receipt["slot"] != slot:
        raise ValueError("receipt schema")
    if receipt["driver_sha256"] != file_identity(__file__)["sha256"]:
        raise ValueError("receipt source")


def receipt_counters(receipt, slot):
    for name in ("received", "retained", "discarded"):
        if type(receipt[name]) is not int or not 0 <= receipt[name] <= COUNTER_MAX:
            raise ValueError("receipt counter")
    if receipt["retained"] > CAPS[slot] or type(receipt["eof"]) is not bool or type(receipt["counter_saturated"]) is not bool:
        raise ValueError("receipt bounds")


def receipt_state(receipt):
    if receipt["state"] not in ("COMPLETE", "INCOMPLETE", "FAILED_IO") or not isinstance(receipt["errors"], list):
        raise ValueError("receipt state")
    if len(receipt["errors"]) > 5 or any(value not in ("READ_ERROR", "WRITE_ERROR", "FLUSH_ERROR", "CLOSE_ERROR") for value in receipt["errors"]):
        raise ValueError("receipt error bounds")
    if not receipt["counter_saturated"] and receipt["received"] != receipt["retained"] + receipt["discarded"]:
        raise ValueError("receipt accounting")
    if receipt["state"] == "COMPLETE" and (not receipt["eof"] or receipt["errors"] or receipt["discarded"] or receipt["counter_saturated"]):
        raise ValueError("false complete receipt")
    if receipt["state"] == "FAILED_IO" and not receipt["errors"]:
        raise ValueError("false IO failure receipt")
    if receipt["state"] == "INCOMPLETE" and not (receipt["discarded"] or receipt["counter_saturated"]):
        raise ValueError("false incomplete receipt")


def load_receipt(directory, slot):
    receipt = json.loads(bounded_read(pathlib.Path(directory) / (slot + RECEIPT_SUFFIX), 4096))
    receipt_shape(receipt, slot)
    receipt_counters(receipt, slot)
    receipt_state(receipt)
    trace_info = (pathlib.Path(directory) / (slot + TRACE_SUFFIX)).lstat()
    if not stat.S_ISREG(trace_info.st_mode) or trace_info.st_size != receipt["retained"]:
        raise ValueError("trace size")
    return receipt


def observe(directory, slot, returncode):
    result = {"tracer_wait_result": {"kind": "SIGNAL" if returncode < 0 else "EXIT",
                                      "value": -returncode if returncode < 0 else returncode},
              "sink_wait_status": "UNOBSERVABLE", "sink_receipt_state": "MISSING",
              "sink_eof_claim": "UNKNOWN", "root_terminal": "UNKNOWN",
              "descendant_join": "UNKNOWN", "pipe_ownership": "UNKNOWN"}
    try:
        receipt = load_receipt(directory, slot)
        result.update({"sink_receipt_state": receipt["state"], "sink_eof_claim": receipt["eof"], "receipt": receipt})
    except FileNotFoundError:
        pass
    except (ValueError, OSError, TypeError):
        result["sink_receipt_state"] = "INVALID"
    return result


def raw_line_facts(line, facts, waits, gaps):
    if len(line) > 4096 or not line.endswith(b"\n"):
        gaps.add("OVERLONG_OR_PARTIAL_LINE")
        return
    if b"unfinished" in line or b"resumed" in line:
        gaps.add("UNPAIRED_OR_UNMODELED_SYSCALL")
    # Retain at most 32 numeric terminal facts; PID is not a generation.
    terminal = re.fullmatch(rb"(?:\[pid\s+)?(\d+)\]?\s+\d+\.\d+\s+\+\+\+ (exited with \d+|killed by SIG[A-Z0-9]+(?: \(core dumped\))?) \+\+\+\n", line)
    if terminal and len(facts) < 32:
        facts.append({"pid_numeric": int(terminal[1]), "terminal": terminal[2].decode("ascii")})
    # strace 6.8 prints the return on the call line. strace 6.19 splits it:
    # "wait4(... <unfinished ...>" then "<... wait4 resumed>) = 0xPID".
    waited = re.search(rb"(?:wait4\([^\n]*\)|<\.\.\. wait4 resumed>\))\s+=\s+(0x[\da-f]+|\d+)\s", line, re.ASCII)
    if waited and len(waits) < 32:
        waits.append({"returned_pid_numeric": int(waited[1], 16 if waited[1].startswith(b"0x") else 10),
                      "wait_status_pointer_contents": "OPAQUE"})


def raw_facts(directory, slot, width):
    """Bounded offline observations only; no pointer decoding or inferred holders."""
    facts = []
    waits = []
    gaps = {"PID_TID_REUSE", "CLONE_FILES", "EXEC_FD_REUSE", "OPAQUE_PIPE_AND_WAIT_STATUS"}
    with (pathlib.Path(directory) / (slot + TRACE_SUFFIX)).open("rb") as stream:
        while stream.tell() <= CAPS[slot]:
            line = stream.readline(4097)
            if not line:
                break
            raw_line_facts(line, facts, waits, gaps)
    return {"abi_word_width": width, "numeric_representation": "strace6.8 raw hexadecimal arguments",
            "terminal_facts": facts, "raw_wait4_facts": waits, "continuity": "UNKNOWN", "gaps": sorted(gaps),
            "tracee_ABI_continuity": "UNKNOWN_ACROSS_EXEC",
            "signed_group_targets": "UNKNOWN_WITHOUT_WITNESSED_ABI_AND_GENERATION"}


def git_command_env():
    env = dict(os.environ)
    for key in tuple(env):
        if key.startswith("GIT_"):
            del env[key]
    env["GIT_NO_REPLACE_OBJECTS"] = "1"
    env["GIT_OPTIONAL_LOCKS"] = "0"
    env["GIT_TERMINAL_PROMPT"] = "0"
    return env


def git(root, *arguments, quiet=False):
    process = subprocess.Popen(["git", "--no-pager", "-c", "core.fsmonitor=false",
                                "-c", "core.hooksPath=/dev/null", "-C", str(root), *arguments],
                               env=git_command_env(), stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL if quiet else None)
    retained = bytearray()
    overflow = False
    try:
        while True:
            chunk = process.stdout.read(4096)
            if not chunk:
                break
            if len(retained) + len(chunk) > 262144:
                overflow = True
            if not overflow:
                retained.extend(chunk)
    finally:
        process.stdout.close()
    if process.wait() or overflow:
        raise ValueError("bounded source Git evidence unavailable")
    return bytes(retained)


def is_ancestor(root, ancestor, commit):
    if ancestor == commit:
        return True
    try:
        git(root, "merge-base", "--is-ancestor", ancestor, commit)
    except ValueError:
        return False
    return True


def assert_test_merge(root, source, base, head, base_ref):
    """Accept GitHub's test merge when main moved after the event snapshot."""
    parents = git(root, "show", "-s", SHOW_PARENTS, source).decode().split()
    if parents == [base, head]:
        return
    if (len(parents) != 2 or parents[1] != head or COMMIT_SHA.fullmatch(parents[0]) is None
            or not isinstance(base_ref, str) or not re.fullmatch(r"[A-Za-z0-9._/-]{1,256}", base_ref)):
        raise ValueError(MERGE_PARENTS)
    tip = git(root, "rev-parse", "--verify", f"refs/remotes/origin/{base_ref}^{{commit}}").decode().strip()
    if not is_ancestor(root, base, parents[0]) or not is_ancestor(root, parents[0], tip):
        raise ValueError(MERGE_PARENTS)


def assert_candidate_tree(root, source, head):
    source_tree = git(root, "rev-parse", source + TREE_SUFFIX).decode().strip()
    if source_tree == git(root, "rev-parse", head + TREE_SUFFIX).decode().strip():
        return
    parents = git(root, "show", "-s", SHOW_PARENTS, source).decode().split()
    if len(parents) != 2 or parents[1] != head:
        raise ValueError(CANDIDATE_TREE)
    try:
        merged = git(root, "merge-tree", "--write-tree", parents[0], head).decode().strip()
    except ValueError:
        raise ValueError(CANDIDATE_TREE) from None
    if merged != source_tree:
        raise ValueError(CANDIDATE_TREE)


def event_context(root):
    env = os.environ
    if env.get("GITHUB_EVENT_NAME") != "pull_request" or env.get("GITHUB_RUN_ATTEMPT") != "1" or env.get("TRACE_BUILD_CHANNEL") != "dev":
        raise ValueError("ineligible event/channel/attempt")
    if env.get("TRACE_ORDINARY_OUTCOME") not in ("success", "failure", "cancelled", "skipped"):
        raise ValueError("missing actual ordinary outcome context")
    event = json.loads(bounded_read(env["GITHUB_EVENT_PATH"], 1048576))
    if event.get("action") not in ("opened", "synchronize"):
        raise ValueError("ineligible action")
    head = event["pull_request"]["head"]["sha"]
    base = event["pull_request"]["base"]["sha"]
    source = env["TRACE_SOURCE_SHA"]
    for value in (head, base, source):
        if COMMIT_SHA.fullmatch(value) is None:
            raise ValueError("missing immutable caller context")
    if git(root, "rev-parse", "HEAD").decode().strip() != source or env.get("GITHUB_SHA") != source:
        raise ValueError("checkout/source mismatch")
    assert_test_merge(root, source, base, head, event["pull_request"]["base"]["ref"])
    parents = git(root, "show", "-s", SHOW_PARENTS, head).decode().split()
    if not parents or any(COMMIT_SHA.fullmatch(item) is None for item in parents):
        raise ValueError("candidate parents are missing")
    assert_candidate_tree(root, source, head)
    git(root, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD")
    if git(root, "ls-files", "--others", "--", "*.go", "go.mod", "go.sum").strip():
        raise ValueError("untracked compile inputs")
    return event, head, base, source, parents


def candidate_contents(root, head, source):
    for name in SOURCE_PATHS:
        actual = bounded_read(root / name, 1048576)
        if stat.S_IMODE((root / name).lstat().st_mode) != 0o644:
            raise ValueError("diagnostic source mode")
        if not git(root, "ls-tree", head, "--", name).startswith(b"100644 blob "):
            raise ValueError("candidate diagnostic source mode")
        if actual != git(root, "show", head + ":" + name) or actual != git(root, "show", source + ":" + name):
            raise ValueError("candidate/merge/source mismatch")


def diagnostic_binding(root):
    workflow = bounded_read(root / SOURCE_PATHS[0], 1048576)
    matches = list(re.finditer(rb"TRACE_DRIVER_SHA256: ([0-9a-f]{64})\n", workflow))
    if len(matches) != 1:
        raise ValueError("workflow binding field")
    match = matches[0]
    actual_driver = file_identity(__file__)["sha256"]
    if match[1].decode() != actual_driver or os.environ.get("TRACE_DRIVER_SHA256") != actual_driver:
        raise ValueError("driver binding mismatch")
    controls = file_identity(root / SOURCE_PATHS[2])
    if controls["mode"] & 0o022:
        raise ValueError("reviewed source binding mismatch")


def original_binding(root, head, source):
    recorded = {}
    for name in HOOK_SOURCES:
        copies = (bounded_read(root / name, 1048576), git(root, "show", head + ":" + name), git(root, "show", source + ":" + name))
        if copies[0] != copies[1] or copies[0] != copies[2]:
            raise ValueError("original source identity changed")
        recorded[name] = digest(copies[0])
    parts = bounded_read(root / "Makefile", 1048576).split(b"hooks-install:\n", 1)
    if len(parts) != 2 or b"\nvscode-extension-install:" not in parts[1]:
        raise ValueError("installer extraction changed")
    installer = parts[1].split(b"\nvscode-extension-install:", 1)[0]
    return recorded, digest(installer)


def commit_exists(root, sha):
    if not isinstance(sha, str) or COMMIT_SHA.fullmatch(sha) is None:
        return False
    try:
        git(root, "cat-file", "-e", sha + "^{commit}", quiet=True)
    except ValueError:
        return False
    return True


def fetch_commit(root, sha):
    command = ["git", "--no-pager", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
               "-C", str(root), "fetch", "--no-tags", "--no-recurse-submodules", "origin", sha]
    try:
        completed = subprocess.run(command, env=git_command_env(), stdout=subprocess.DEVNULL,
                                   stderr=subprocess.DEVNULL, timeout=30, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return completed.returncode == 0 and commit_exists(root, sha)


def base_merge_anchor(root, event, head):
    pull = event.get("pull_request")
    base = pull.get("base") if isinstance(pull, dict) else None
    if not isinstance(base, dict):
        raise ValueError(UNREACHABLE_PREDECESSOR)
    tip = base.get("sha")
    if not commit_exists(root, tip):
        ref = base.get("ref")
        if not isinstance(ref, str) or re.fullmatch(r"[A-Za-z0-9._/-]{1,256}", ref) is None or ".." in ref.split("/"):
            raise ValueError(UNREACHABLE_PREDECESSOR)
        try:
            tip = git(root, "rev-parse", "--verify", f"refs/remotes/origin/{ref}^{{commit}}", quiet=True).decode().strip()
        except ValueError:
            raise ValueError(UNREACHABLE_PREDECESSOR) from None
    if not commit_exists(root, tip) or not commit_exists(root, head):
        raise ValueError(UNREACHABLE_PREDECESSOR)
    try:
        anchor = git(root, "merge-base", tip, head, quiet=True).decode().strip()
    except ValueError:
        raise ValueError(UNREACHABLE_PREDECESSOR) from None
    if not commit_exists(root, anchor):
        raise ValueError(UNREACHABLE_PREDECESSOR)
    return anchor


def synchronize_anchor(root, before, head, event):
    if commit_exists(root, before) or fetch_commit(root, before):
        return before
    return base_merge_anchor(root, event, head)


def synchronize_binding(root, event, head):
    if event["action"] != "synchronize":
        return False
    before = event.get("before")
    if not isinstance(before, str) or COMMIT_SHA.fullmatch(before) is None or before == head or event.get("after") != head:
        raise ValueError("missing or identical synchronize context")
    anchor = synchronize_anchor(root, before, head, event)
    try:
        changed = git(root, "diff", "--name-only", anchor, head, "--", *SOURCE_PATHS, quiet=True)
    except ValueError:
        raise ValueError(UNREACHABLE_PREDECESSOR) from None
    return bool(changed.strip())


def validate_binding(root):
    event, head, base, source, parents = event_context(root)
    candidate_contents(root, head, source)
    diagnostic_binding(root)
    originals, installer = original_binding(root, head, source)
    return {"parents": parents, "head": head, "base": base, "checkout_merge": source,
            "checkout_tree": git(root, "rev-parse", source + TREE_SUFFIX).decode().strip(),
            "action": event["action"], "originals": originals, "installer_sha256": installer,
            "diagnostic_sources_changed": synchronize_binding(root, event, head),
            "review_authority_claim": False}


def tracer_identity(result):
    tracer = file_identity(STRACE)
    result["preflight"].update({"strace": tracer, "image": os.environ.get("ImageVersion", "")[:256],
                                "kernel": platform.release(), "architecture": platform.machine()})
    image = os.environ.get("ImageVersion", "")
    if not IMAGE_VERSION.fullmatch(image):
        raise ValueError("hosted image identity unavailable")
    reviewed = next((item for item in REVIEWED_TRACERS if item[0] == tracer["sha256"]), None)
    if reviewed is None:
        raise ValueError("unreviewed tracer")
    if tracer["uid"] != 0 or tracer["mode"] & 0o022:
        raise ValueError("tracer ownership/mode")
    version = subprocess.check_output([STRACE, "-V"], stderr=subprocess.STDOUT)
    package = subprocess.check_output(["/usr/bin/dpkg-query", "-W", "-f=${Version} ${Architecture}", "strace"])
    result["preflight"].update({"strace_version": version[:4096].decode(errors="replace"), "dpkg": package[:256].decode(errors="replace")})
    if len(version) > 4096 or reviewed[1] not in version or package != reviewed[2]:
        raise ValueError("tracer version/package")
    return tracer, version, package


def preflight(root, result):
    result["preflight"] = {"platform": sys.platform}
    if sys.platform != "linux" or platform.machine() != "x86_64":
        raise ValueError("native Linux amd64 required")
    interpreter = file_identity(PYTHON)
    result["preflight"].update({"python": interpreter, "python_version": sys.version})
    if interpreter["realpath"].startswith(str(root) + "/") or not sys.flags.isolated or not sys.flags.no_site:
        raise ValueError("interpreter isolation")
    if interpreter["uid"] != 0 or interpreter["mode"] & 0o022:
        raise ValueError("interpreter ownership/mode")
    import json as stdlib
    origin = pathlib.Path(stdlib.__file__).resolve()
    if origin.is_relative_to(root):
        raise ValueError("stdlib inside checkout")
    tracer, version, package = tracer_identity(result)
    yama = bounded_read("/proc/sys/kernel/yama/ptrace_scope", 64).decode().strip()
    if yama not in ("0", "1"):
        raise ValueError("unprivileged tracing unavailable")
    own = {}
    for line in bounded_read("/proc/self/status", 8192).decode().splitlines():
        key, _, value = line.partition(":")
        if key in ("Uid", "Gid", "CapEff", "NoNewPrivs", "Seccomp"):
            own[key] = value.strip()
    if os.geteuid() == 0 or int(own["CapEff"], 16) != 0:
        raise ValueError("unprivileged native owner required")
    return {"python": interpreter, "python_version": sys.version, "stdlib_origin": str(origin),
            "strace": tracer, "strace_version": version.decode(), "dpkg": package.decode(),
            "image": os.environ["ImageVersion"], "kernel": platform.release(), "yama": yama, "own_process": own}


def inventory(directory):
    records = []
    seen = set()
    paths = []
    with os.scandir(directory) as entries:
        for entry in entries:
            if len(paths) == 23:
                raise ValueError("upload file count bound")
            paths.append(pathlib.Path(entry.path))
    for path in sorted(paths):
        name = path.name
        slot = name[:-4] if name.endswith(".tmp") else name
        info = path.lstat()
        if slot not in SLOTS or slot in seen or not stat.S_ISREG(info.st_mode) or info.st_size > SLOTS[slot]:
            raise ValueError("upload allowlist/allocation violation")
        seen.add(slot)
        records.append({"name": name, "slot": slot, "bytes": info.st_size, "sha256": file_identity(path)["sha256"]})
    if sum(item["bytes"] for item in records) > MAX_RETAINED or len(records) > 23:
        raise ValueError("whole upload bound")
    return records


def module_go_version(root):
    match = re.search(rb"(?m)^go (\d+\.\d+\.\d+)\s*$", bounded_read(root / "go.mod", 65536))
    if not match:
        raise ValueError("go.mod toolchain version")
    return b"go" + match.group(1)


# Official go1.27.4 linux-amd64 release from https://go.dev/dl/go1.27.4.linux-amd64.tar.gz.
# The tarball checksum is the value published by go.dev. The tree digest is SHA256 over
# sorted "relative-path\0file-sha256\n" records for every regular file in that archive's
# go/ directory. ubuntu-26.04 image builds run chmod -R 777 /opt, so the toolcache copy
# is mode 0777; seal_toolcache_toolchain copies it privately and fails closed on mismatch.
OFFICIAL_GO_VERSION = b"go1.27.4"
OFFICIAL_GO_URL = "https://go.dev/dl/go1.27.4.linux-amd64.tar.gz"
OFFICIAL_GO_BYTES = 70680823
OFFICIAL_GO_HOSTS = ("dl.google.com", "go.dev")
OFFICIAL_GO_TARBALL_SHA256 = "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"
OFFICIAL_GO_TREE_SHA256 = "e03103468fede9c84b22d727ed374e563f27de5647a7f7a84cf299ab7b42595e"
MAX_ARCHIVE_MEMBERS = 25000
COMPILER_CUSTODY = "compiler executable custody"
COMPILER_INTEGRITY = "compiler integrity mismatch"
ZERO_WRITE = "zero write"
MAX_TOOLCHAIN_FILES = 20000
MAX_TOOLCHAIN_BYTES = 300000000
FIXTURE_MODE = int("777", 8)


def sealed_file_mode(relative):
    if relative.startswith(("bin/", "pkg/tool/")):
        return 0o750
    return 0o640


def ensure_private_dir(path, root):
    path.mkdir(parents=True, exist_ok=True)
    current = path
    while True:
        os.chmod(current, 0o750)
        info = current.lstat()
        if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o022:
            raise ValueError(COMPILER_CUSTODY)
        if current == root:
            return
        current = current.parent


def reject_source_file(info):
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError(COMPILER_CUSTODY)
    if info.st_size > MAX_TOOLCHAIN_BYTES:
        raise ValueError(COMPILER_INTEGRITY)


def confirm_same_file(fd, info):
    current = os.fstat(fd)
    if not stat.S_ISREG(current.st_mode) or (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
        raise ValueError(COMPILER_CUSTODY)


def write_view(out, chunk):
    view = memoryview(chunk)
    while view:
        wrote = os.write(out, view)
        if wrote <= 0:
            raise OSError(ZERO_WRITE)
        view = view[wrote:]


def write_stream(src, dest):
    out = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o640)
    try:
        total = 0
        while True:
            chunk = os.read(src, 1024 * 1024)
            if not chunk:
                return total
            total += len(chunk)
            if total > MAX_TOOLCHAIN_BYTES:
                raise ValueError(COMPILER_INTEGRITY)
            write_view(out, chunk)
    finally:
        os.close(out)


def confirm_mode(path, mode):
    info = os.lstat(path)
    if stat.S_ISLNK(info.st_mode) or stat.S_IMODE(info.st_mode) != mode:
        raise ValueError(COMPILER_CUSTODY)


def copy_regular(source, dest, mode):
    info = os.lstat(source)
    reject_source_file(info)
    src = os.open(source, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        confirm_same_file(src, info)
        total = write_stream(src, dest)
    finally:
        os.close(src)
    os.chmod(dest, mode)
    confirm_mode(dest, mode)
    return total


def list_tree_files(root):
    files = []
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        dirnames.sort()
        for name in sorted(filenames):
            path = pathlib.Path(dirpath) / name
            relative = path.relative_to(root).as_posix()
            if not relative or relative.startswith("../") or "/../" in relative:
                raise ValueError(COMPILER_CUSTODY)
            files.append((relative, path))
            if len(files) > MAX_TOOLCHAIN_FILES:
                raise ValueError(COMPILER_INTEGRITY)
    if not files:
        raise ValueError(COMPILER_CUSTODY)
    return files


def toolchain_tree_digest(root):
    """Content digest of a toolchain tree. Modes are not part of the digest."""
    records = []
    total = 0
    for relative, path in list_tree_files(root):
        file_hash, size = hash_regular(path)
        total += size
        if total > MAX_TOOLCHAIN_BYTES:
            raise ValueError(COMPILER_INTEGRITY)
        records.append(relative + "\0" + file_hash + "\n")
    records.sort()
    return hashlib.sha256("".join(records).encode()).hexdigest()


def hash_regular(path):
    info = os.lstat(path)
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError(COMPILER_CUSTODY)
    src = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        current = os.fstat(src)
        if not stat.S_ISREG(current.st_mode) or (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
            raise ValueError(COMPILER_CUSTODY)
        hasher = hashlib.sha256()
        total = 0
        while True:
            chunk = os.read(src, 1024 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if total > MAX_TOOLCHAIN_BYTES:
                raise ValueError(COMPILER_INTEGRITY)
            hasher.update(chunk)
        return hasher.hexdigest(), total
    finally:
        os.close(src)


def seal_toolcache_toolchain(source, destination, expected_tree):
    """Copy source into a private tree with safe modes and require the official content digest.

    The source may be mode 0777. This never chmods it and never executes it. A content
    mismatch, symlink, or writable private file removes the private tree and fails closed.
    """
    if not isinstance(expected_tree, str) or re.fullmatch(r"[0-9a-f]{64}", expected_tree) is None:
        raise ValueError(COMPILER_INTEGRITY)
    source = pathlib.Path(source)
    destination = pathlib.Path(destination)
    if source.is_symlink() or not source.is_dir() or destination.exists():
        raise ValueError(COMPILER_CUSTODY)
    source_resolved = source.resolve(strict=True)
    parent = destination.parent.resolve(strict=True)
    if parent == source_resolved or parent.is_relative_to(source_resolved):
        raise ValueError(COMPILER_CUSTODY)
    try:
        ensure_private_dir(destination, destination)
        total = 0
        for relative, path in list_tree_files(source):
            dest = destination.joinpath(*relative.split("/"))
            ensure_private_dir(dest.parent, destination)
            total += copy_regular(path, dest, sealed_file_mode(relative))
            if total > MAX_TOOLCHAIN_BYTES:
                raise ValueError(COMPILER_INTEGRITY)
        if toolchain_tree_digest(destination) != expected_tree:
            raise ValueError(COMPILER_INTEGRITY)
        private = destination / "bin" / "go"
        info = private.lstat()
        if private.is_symlink() or not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o750:
            raise ValueError(COMPILER_CUSTODY)
        if not private.resolve(strict=True).is_relative_to(destination.resolve(strict=True)):
            raise ValueError(COMPILER_CUSTODY)
        return private
    except Exception:
        shutil.rmtree(destination, ignore_errors=True)
        raise


def archive_relative(name):
    if name.startswith("./"):
        name = name[2:]
    if name.endswith("/"):
        name = name[:-1]
    if name in ("", "go"):
        return ""
    if not name.startswith("go/"):
        raise ValueError(COMPILER_INTEGRITY)
    relative = name[3:]
    if not relative or any(part in ("", ".", "..") for part in relative.split("/")):
        raise ValueError(COMPILER_INTEGRITY)
    return relative


def write_bytes_private(dest, data, mode):
    fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o640)
    try:
        view = memoryview(data)
        while view:
            wrote = os.write(fd, view)
            if wrote <= 0:
                raise OSError(ZERO_WRITE)
            view = view[wrote:]
    finally:
        os.close(fd)
    os.chmod(dest, mode)
    confirm_mode(dest, mode)


def copy_archive_member(archive, member, destination):
    relative = archive_relative(member.name)
    if member.issym() or member.islnk() or not (member.isdir() or member.isfile()):
        raise ValueError(COMPILER_INTEGRITY)
    if not relative:
        return
    dest = destination.joinpath(*relative.split("/"))
    if member.isdir():
        ensure_private_dir(dest, destination)
        return
    extracted = archive.extractfile(member)
    if extracted is None or member.size > MAX_TOOLCHAIN_BYTES:
        raise ValueError(COMPILER_INTEGRITY)
    data = extracted.read(MAX_TOOLCHAIN_BYTES + 1)
    if len(data) != member.size:
        raise ValueError(COMPILER_INTEGRITY)
    ensure_private_dir(dest.parent, destination)
    write_bytes_private(dest, data, sealed_file_mode(relative))


def extract_verified_toolchain(payload, destination, expected_sha, expected_tree):
    """Extract an official Go tarball only after its checksum and tree digest match."""
    if hashlib.sha256(payload).hexdigest() != expected_sha:
        raise ValueError(COMPILER_INTEGRITY)
    destination = pathlib.Path(destination)
    if destination.exists():
        raise ValueError(COMPILER_CUSTODY)
    import io
    import tarfile
    try:
        ensure_private_dir(destination, destination)
        with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as archive:
            count = 0
            for member in archive:
                count += 1
                if count > MAX_ARCHIVE_MEMBERS:
                    raise ValueError(COMPILER_INTEGRITY)
                copy_archive_member(archive, member, destination)
        if toolchain_tree_digest(destination) != expected_tree:
            raise ValueError(COMPILER_INTEGRITY)
        private = destination / "bin" / "go"
        info = private.lstat()
        if private.is_symlink() or not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o750:
            raise ValueError(COMPILER_CUSTODY)
        return private
    except Exception:
        shutil.rmtree(destination, ignore_errors=True)
        raise


def read_official_tarball():
    import urllib.error
    import urllib.request
    request = urllib.request.Request(OFFICIAL_GO_URL, method="GET")
    try:
        response = urllib.request.urlopen(request, timeout=120)
    except (OSError, urllib.error.URLError) as error:
        raise ValueError(COMPILER_INTEGRITY) from error
    with response:
        host = urllib.parse.urlparse(response.geturl()).hostname
        if response.status != 200 or host not in OFFICIAL_GO_HOSTS:
            raise ValueError(COMPILER_INTEGRITY)
        payload = bytearray()
        while len(payload) <= OFFICIAL_GO_BYTES:
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            payload.extend(chunk)
    if len(payload) != OFFICIAL_GO_BYTES or hashlib.sha256(payload).hexdigest() != OFFICIAL_GO_TARBALL_SHA256:
        raise ValueError(COMPILER_INTEGRITY)
    return bytes(payload)


def private_toolchain(source, destination, expected_tree, fetch):
    """Use a matching toolcache copy, otherwise a checksum-verified official archive."""
    try:
        return seal_toolcache_toolchain(source, destination, expected_tree), "toolcache"
    except ValueError as error:
        if str(error) != COMPILER_INTEGRITY:
            raise
    payload, digest = fetch()
    return extract_verified_toolchain(payload, destination, digest, expected_tree), "official-tarball"


def assert_official_pins():
    if OFFICIAL_GO_TARBALL_SHA256 != "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840":
        raise AssertionError("official Go tarball pin drifted")
    if OFFICIAL_GO_TREE_SHA256 != "e03103468fede9c84b22d727ed374e563f27de5647a7f7a84cf299ab7b42595e":
        raise AssertionError("official Go tree pin drifted")


def assert_private_modes(source, destination, private):
    if private != destination / "bin" / "go" or private.read_bytes() != b"official-go":
        raise AssertionError("private compiler is not the copied toolchain")
    compiler = destination / "pkg" / "tool" / "linux_amd64" / "compile"
    version = destination / "VERSION"
    modes = (stat.S_IMODE(private.lstat().st_mode), stat.S_IMODE(compiler.lstat().st_mode), stat.S_IMODE(version.lstat().st_mode))
    if modes != (0o750, 0o750, 0o640):
        raise AssertionError("private toolchain modes are not safe")
    if stat.S_IMODE((source / "bin" / "go").lstat().st_mode) != FIXTURE_MODE:
        raise AssertionError("toolcache mode was changed in place")
    if (source / "bin" / "go").read_bytes() != b"official-go":
        raise AssertionError("toolcache bytes were rewritten")


def assert_writable_toolcache_matches():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "toolcache"
        write_fixture_toolchain(source, b"official-go", FIXTURE_MODE)
        destination = directory / "private"
        private = seal_toolcache_toolchain(source, destination, toolchain_tree_digest(source))
        assert_private_modes(source, destination, private)


def assert_tampered_toolchain_rejected():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "toolcache"
        write_fixture_toolchain(source, b"official-go", FIXTURE_MODE)
        expected = toolchain_tree_digest(source)
        tampered = source / "bin" / "go"
        tampered.write_bytes(b"tampered-go")
        os.chmod(tampered, FIXTURE_MODE)
        destination = directory / "rejected"
        try:
            seal_toolcache_toolchain(source, destination, expected)
        except ValueError as error:
            if str(error) != COMPILER_INTEGRITY:
                raise AssertionError("tampered toolchain error was " + str(error)) from error
        else:
            raise AssertionError("tampered toolchain accepted")
        if destination.exists():
            raise AssertionError("tampered toolchain copy was kept")


def fixture_archive(root):
    import io
    import tarfile
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for relative, path in list_tree_files(root):
            archive.add(path, arcname="go/" + relative, recursive=False)
    return buffer.getvalue()


def assert_matching_toolcache_skips_download():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "toolcache"
        write_fixture_toolchain(source, b"official-go", FIXTURE_MODE)
        expected = toolchain_tree_digest(source)

        def fetch():
            raise AssertionError("official archive fetched for a matching toolcache")

        private, origin = private_toolchain(source, directory / "private", expected, fetch)
        if origin != "toolcache" or private.read_bytes() != b"official-go":
            raise AssertionError("matching toolcache was not sealed in place")


def assert_extra_file_uses_official_archive():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "toolcache"
        write_fixture_toolchain(source, b"official-go", FIXTURE_MODE)
        expected = toolchain_tree_digest(source)
        archive = fixture_archive(source)
        digest = hashlib.sha256(archive).hexdigest()
        extra = source / "setup.sh"
        extra.write_bytes(b"installer\n")
        os.chmod(extra, FIXTURE_MODE)
        destination = directory / "private"
        private, origin = private_toolchain(source, destination, expected, lambda: (archive, digest))
        if origin != "official-tarball" or private.read_bytes() != b"official-go":
            raise AssertionError("extra installer did not fall back to the official archive")
        if (destination / "setup.sh").exists() or toolchain_tree_digest(destination) != expected:
            raise AssertionError("fallback tree is not the official file set")
        if extra.read_bytes() != b"installer\n" or stat.S_IMODE(extra.lstat().st_mode) != FIXTURE_MODE:
            raise AssertionError("toolcache installer was modified")


def assert_unverified_archive_rejected():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "toolcache"
        write_fixture_toolchain(source, b"official-go", FIXTURE_MODE)
        expected = toolchain_tree_digest(source)
        archive = bytearray(fixture_archive(source))
        extra = source / "setup.sh"
        extra.write_bytes(b"installer\n")
        os.chmod(extra, FIXTURE_MODE)
        archive[min(32, len(archive) - 1)] ^= 1
        digest = hashlib.sha256(bytes(archive) + b"x").hexdigest()
        destination = directory / "rejected"
        try:
            private_toolchain(source, destination, expected, lambda: (bytes(archive), digest))
        except ValueError as error:
            if str(error) != COMPILER_INTEGRITY:
                raise AssertionError("unverified archive error was " + str(error)) from error
        else:
            raise AssertionError("unverified archive accepted")
        if destination.exists():
            raise AssertionError("unverified archive copy was kept")
        if (source / "bin" / "go").read_bytes() != b"official-go":
            raise AssertionError("rejected toolcache bytes were rewritten")


def assert_compiler_custody(mode):
    assert_official_pins()
    checks = {
        "admit": assert_writable_toolcache_matches,
        "tamper": assert_tampered_toolchain_rejected,
        "fallback": assert_extra_file_uses_official_archive,
        "bad-archive": assert_unverified_archive_rejected,
        "skip-download": assert_matching_toolcache_skips_download,
    }
    check = checks.get(mode)
    if check is None:
        raise AssertionError("unknown compiler custody mode")
    check()


def write_fixture_toolchain(root, go_bytes, mode):
    files = {
        "bin/go": go_bytes,
        "pkg/tool/linux_amd64/compile": b"official-compile",
        "VERSION": b"go1.27.4\n",
    }
    for relative, content in files.items():
        path = root.joinpath(*relative.split("/"))
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
    for dirpath, dirnames, filenames in os.walk(root):
        os.chmod(dirpath, mode)
        for name in dirnames + filenames:
            os.chmod(pathlib.Path(dirpath) / name, mode)


def toolcache_source(root):
    located = shutil.which("go")
    if not located:
        raise ValueError(COMPILER_CUSTODY)
    go_path = pathlib.Path(located)
    if go_path.is_symlink() or not go_path.is_file():
        raise ValueError(COMPILER_CUSTODY)
    if not go_path.is_absolute():
        go_path = pathlib.Path.cwd() / go_path
    go_path = go_path.absolute()
    if go_path.name != "go" or go_path.parent.name != "bin" or go_path.is_relative_to(root):
        raise ValueError(COMPILER_CUSTODY)
    source = go_path.parent.parent
    if source.is_relative_to(root) or root.is_relative_to(source):
        raise ValueError(COMPILER_CUSTODY)
    return source


def compile_normal(root, build, result):
    if os.environ.get("GOFLAGS") or os.environ.get("GOEXPERIMENT"):
        raise ValueError("unreviewed compile flags")
    required = module_go_version(root)
    if required != OFFICIAL_GO_VERSION:
        raise ValueError("compiler version")
    # Copy first. The image toolcache is writable, so its go binary is not executed.
    source = toolcache_source(root)
    destination = build / "goroot"
    private, origin = private_toolchain(
        source, destination, OFFICIAL_GO_TREE_SHA256,
        lambda: (read_official_tarball(), OFFICIAL_GO_TARBALL_SHA256))
    env = dict(os.environ, GOTOOLCHAIN="local", GOENV="off", GOPROXY="off", GOSUMDB="off", GOROOT=str(destination))
    result["go_executable"] = file_identity(private)
    if result["go_executable"]["mode"] & 0o022:
        raise ValueError(COMPILER_CUSTODY)
    go_version = subprocess.check_output([str(private), "version"], env=env)
    result["go_version"] = go_version[:4096].decode(errors="replace")
    if not go_version.startswith(b"go version " + required + b" "):
        raise ValueError("compiler version")
    compiler = destination / "pkg" / "tool" / "linux_amd64" / "compile"
    result["compiler"] = file_identity(compiler)
    if result["compiler"]["mode"] & 0o022:
        raise ValueError(COMPILER_CUSTODY)
    result["go_version"] = go_version.decode()
    result["go_toolchain"] = {"origin": origin, "tarball_sha256": OFFICIAL_GO_TARBALL_SHA256, "tree_sha256": OFFICIAL_GO_TREE_SHA256}
    binary = build / "hook.test"
    compile_status = subprocess.call([str(private), "test", "-c", "-ldflags", "-X github.com/ben-ranford/lopper/internal/version.buildChannel=dev", "-o", str(binary), "./scripts"], cwd=root, env=env)
    result["compile_status"] = compile_status
    if compile_status:
        raise ValueError("normal compile failed")
    result["binary"] = file_identity(binary)
    buildinfo = subprocess.check_output([str(private), "version", "-m", str(binary)], env=env)
    if len(buildinfo) > 8192:
        raise ValueError("buildinfo bound")
    result["buildinfo"] = buildinfo.decode()
    return binary


def run():
    root = pathlib.Path(__file__).resolve().parent.parent
    temp = pathlib.Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    if temp.is_relative_to(root):
        raise ValueError("runner temp inside checkout")
    identifiers = [os.environ[name] for name in ("GITHUB_RUN_ID", "GITHUB_JOB", "GITHUB_RUN_ATTEMPT")]
    if any(not re.fullmatch("[A-Za-z0-9_-]{1,64}", value) for value in identifiers):
        raise ValueError("invalid output identifiers")
    name = "hook-native-" + "-".join(identifiers)
    output = temp / name
    output.mkdir(mode=0o700)
    result = {"version": 1, "state": "UNAVAILABLE", "ordinary_outcome": os.environ.get("TRACE_ORDINARY_OUTCOME"),
              "sink_wait_status": "UNOBSERVABLE", "historical_cause": "UNKNOWN"}
    status = 1
    try:
        if os.statvfs(output).f_bavail * os.statvfs(output).f_frsize < MAX_RETAINED:
            raise ValueError("insufficient free-space preflight")
        result["binding"] = validate_binding(root)
        result["preflight"] = preflight(root, result)
        build = temp / (name + "-build")
        build.mkdir(mode=0o700)
        binary = compile_normal(root, build, result)
        controls = root / SOURCE_PATHS[2]
        # Fixed control owner owns only private children; no focused supervisor.
        control_status = subprocess.call([PYTHON, "-I", "-S", str(controls), "controls", str(output), str(build)])
        result["controls_status"] = control_status
        result["control_truth"] = json.loads(bounded_read(build / "control-truth.json", 16384))
        if control_status:
            raise ValueError("private controls failed")
        target = [str(binary), "-test.run=" + SELECTOR, "-test.count=1", "-test.timeout=10m"]
        command = trace_command(pathlib.Path(__file__).resolve(), output, "focused", target)
        result["focused_argv"] = command
        # Persist exact launch provenance before launch in the one bounded log slot.
        exclusive_write(output, LOG_NAME, (json.dumps({"command": command, "driver": file_identity(__file__), "control_truth": result["control_truth"]}) + "\n").encode())
        tracer = subprocess.Popen(command, cwd=root / "scripts")
        returncode = tracer.wait()  # Only our direct strace child. No timeout/target cleanup.
        result["focused"] = observe(output, "focused", returncode)
        result["focused"]["raw_facts"] = raw_facts(output, "focused", 64)
        result["state"] = "COMPLETE" if result["focused"]["sink_receipt_state"] == "COMPLETE" else "UNQUALIFIED"
        if result["state"] == "COMPLETE":
            status = returncode
            if returncode < 0:
                status = 128 - returncode
    except (Exception, KeyboardInterrupt) as error:
        if isinstance(error, KeyboardInterrupt):
            result["state"] = "PARTIAL_UNKNOWN"
        result["error"] = type(error).__name__
        result["reason"] = str(error)[:1024]
    finally:
        publish(output, MAIN_NAME, result)
        publish(output, MANIFEST_NAME, {"version": 1, "content_ceiling": MAX_RETAINED, "slots": SLOTS,
                                          "files_before_manifest": inventory(output), "sink_wait_status": "UNOBSERVABLE"})
        inventory(output)
    return status


def selftest():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        source = directory / "source"
        source.write_bytes(b"a" * 20000)
        source_fd = os.open(source, os.O_RDONLY)
        target_fd = os.open(directory / "target", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            result = drain(source_fd, target_fd, 8192)
        finally:
            os.close(source_fd)
            os.close(target_fd)
        assert result["received"] == 20000 and result["retained"] == 8192 and result["discarded"] == 11808 and result["eof"]
        assert (directory / "target").stat().st_size == 8192
        publish(directory, MAIN_NAME, {"portable_support_only": True})
        try:
            publish(directory, MAIN_NAME, {})
        except FileExistsError:
            pass
        else:
            raise AssertionError("existing slot accepted")
        source_fd = os.open(source, os.O_RDONLY)
        readonly_fd = os.open(source, os.O_RDONLY)
        try:
            failure = drain(source_fd, readonly_fd, 8192)
        finally:
            os.close(source_fd)
            os.close(readonly_fd)
        assert failure["errors"] == ["WRITE_ERROR"] and failure["eof"]
        assert failure["received"] == failure["discarded"] == 20000 and failure["retained"] == 0
        try:
            exclusive_write(directory, LOG_NAME, b"x" * 65537)
        except ValueError:
            pass
        else:
            raise AssertionError("metadata overflow accepted")
        assert not (directory / "preflight.log.tmp").exists()
        original_rename = os.rename
        def failed_rename(*_, **_kwargs):
            raise OSError("portable rename failure seam")
        try:
            os.rename = failed_rename
            publish(directory, MANIFEST_NAME, {"bounded": True})
        except OSError:
            pass
        finally:
            os.rename = original_rename
        assert (directory / "manifest.json.tmp").is_file() and not (directory / MANIFEST_NAME).exists()
        # Receipt alone never supplies a waited sink outcome; corrupt accounting fails.
        (directory / "exit0.trace").write_bytes(b"")
        receipt = {"version": 1, "slot": "exit0", "driver_sha256": file_identity(__file__)["sha256"],
                   "state": "COMPLETE", "received": 0, "retained": 0, "discarded": 0,
                   "counter_saturated": False, "eof": True, "errors": []}
        (directory / "exit0.receipt.json").write_text(json.dumps(receipt))
        assert observe(directory, "exit0", 0)["sink_receipt_state"] == "COMPLETE"
        assert observe(directory, "exit0", 0)["sink_wait_status"] == "UNOBSERVABLE"
        receipt["received"] = 1
        (directory / "exit0.receipt.json").write_text(json.dumps(receipt))
        assert observe(directory, "exit0", 0)["sink_receipt_state"] == "INVALID"
        (directory / TRACE_NAMES["exit23"]).write_bytes(b"12 1.000000 wait4(0xd, 0x1234, 0x0, 0x0) = 0xd <0.01>\n12 1.000001 +++ exited with 23 +++\n")
        facts = raw_facts(directory, "exit23", 64)
        assert facts["raw_wait4_facts"] == [{"returned_pid_numeric": 13, "wait_status_pointer_contents": "OPAQUE"}]
        assert facts["terminal_facts"] == [{"pid_numeric": 12, "terminal": "exited with 23"}]
        assert facts["continuity"] == "UNKNOWN"
        (directory / TRACE_NAMES["exit23"]).write_bytes(b"12 1.000000 wait4(0x48d6f, 0x7fff65e0e77c, 0, 0 <unfinished ...>\n12 1.000001 <... wait4 resumed>) = 0x48d6f <0.000261>\n")
        facts = raw_facts(directory, "exit23", 64)
        assert facts["raw_wait4_facts"] == [{"returned_pid_numeric": 298351, "wait_status_pointer_contents": "OPAQUE"}]
    assert len(SLOTS) == 23 and sum(SLOTS.values()) == MAX_RETAINED
    assert_compiler_custody("admit")
    assert_compiler_custody("tamper")
    assert_compiler_custody("fallback")
    assert_compiler_custody("bad-archive")
    assert_compiler_custody("skip-download")
    assert observe("/nonexistent-native-trace-selftest", "exit0", 23)["tracer_wait_result"] == {"kind": "EXIT", "value": 23}
    assert observe("/nonexistent-native-trace-selftest", "signal", -15)["sink_wait_status"] == "UNOBSERVABLE"
    prior_pin = os.environ.get("TRACE_DRIVER_SHA256")
    try:
        os.environ["TRACE_DRIVER_SHA256"] = file_identity(__file__)["sha256"]
        diagnostic_binding(pathlib.Path(__file__).resolve().parent.parent)
        os.environ["TRACE_DRIVER_SHA256"] = "0" * 64
        try:
            diagnostic_binding(pathlib.Path(__file__).resolve().parent.parent)
        except ValueError:
            pass
        else:
            raise AssertionError("bad real driver binding accepted")
    finally:
        if prior_pin is None:
            os.environ.pop("TRACE_DRIVER_SHA256", None)
        else:
            os.environ["TRACE_DRIVER_SHA256"] = prior_pin
    assert_moved_base_merge()
    assert_synchronize_predecessor()
    print("portable support checks PASS; native controls NOT executed")
    return 0


def portable_git(repo, *args, output=False):
    result = subprocess.run(["git", "-c", "protocol.file.allow=always", "-c", "user.email=trace@example.com",
                             "-c", "user.name=trace", "-C", str(repo), *args], check=True,
                            stdout=subprocess.PIPE if output else subprocess.DEVNULL, text=True)
    return result.stdout.strip() if output else None


def synchronize_event(before_sha, head_sha, base_sha, ref="main"):
    return {"action": "synchronize", "before": before_sha, "after": head_sha,
            "pull_request": {"base": {"sha": base_sha, "ref": ref}}}


def expect_commit(root, sha, present, reason):
    if commit_exists(root, sha) is not present:
        raise AssertionError(reason)


def expect_binding(root, event, head, expected, reason):
    if synchronize_binding(root, event, head) is not expected:
        raise AssertionError(reason)


def expect_binding_error(root, event, head, message, reason):
    try:
        synchronize_binding(root, event, head)
    except ValueError as error:
        if str(error) != message:
            raise
        return
    raise AssertionError(reason)


def prepare_replaced_tip(directory):
    seed = directory / "seed"
    origin = directory / "origin.git"
    work = directory / "work"
    seed.mkdir()
    subprocess.run(["git", "init", "-q", "-b", "main", str(seed)], check=True)
    source = seed / SOURCE_PATHS[1]
    source.parent.mkdir(parents=True)
    source.write_text("base\n")
    portable_git(seed, "add", SOURCE_PATHS[1])
    portable_git(seed, "commit", "-q", "-m", "base")
    base = portable_git(seed, "rev-parse", "HEAD", output=True)
    source.write_text("same\n")
    portable_git(seed, "commit", "-q", "-am", "before")
    before = portable_git(seed, "rev-parse", "HEAD", output=True)
    portable_git(seed, "reset", "-q", "--hard", base)
    source.write_text("same\n")
    portable_git(seed, "commit", "-q", "-am", "head")
    head = portable_git(seed, "rev-parse", "HEAD", output=True)
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(origin)], check=True)
    portable_git(origin, "config", "uploadpack.allowReachableSHA1InWant", "true")
    portable_git(origin, "config", "uploadpack.allowAnySHA1InWant", "true")
    portable_git(seed, "remote", "add", "origin", str(origin))
    portable_git(seed, "push", "-q", "origin", "HEAD:main")
    portable_git(seed, "push", "-q", "origin", before + ":refs/heads/replaced-tip")
    subprocess.run(["git", "-c", "protocol.file.allow=always", "clone", "-q", "--no-local", "--single-branch",
                    "--branch", "main", str(origin), str(work)], check=True)
    portable_git(work, "config", "protocol.file.allow", "always")
    expect_commit(work, before, False, "single-branch clone contained the replaced tip")
    return work, before, head, base


def assert_synchronize_predecessor():
    import tempfile
    missing = "0123456789abcdef0123456789abcdef01234567"
    absent_base = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
    with tempfile.TemporaryDirectory() as directory:
        work, before, head, base = prepare_replaced_tip(pathlib.Path(directory))
        # The replaced tip matches head, so only a fetched before proves the diff is empty.
        expect_binding(work, synchronize_event(before, head, base), head, False,
                       "fetched predecessor did not bound an empty diagnostic diff")
        expect_commit(work, before, True, "reachable predecessor was not fetched")
        expect_binding(work, synchronize_event(missing, head, base), head, True,
                       "merge-base fallback missed a diagnostic source change")
        expect_commit(work, missing, False, "missing predecessor appeared without evidence")
        portable_git(work, "update-ref", "refs/remotes/origin/main", base)
        expect_binding(work, synchronize_event(missing, head, absent_base), head, True,
                       "base branch ref did not provide a merge-base")
        expect_binding_error(work, synchronize_event(missing, head, absent_base, "no-such-base"), head,
                             UNREACHABLE_PREDECESSOR, "unbounded synchronize predecessor accepted")
        (work / "other.txt").write_text("other\n")
        portable_git(work, "add", "other.txt")
        portable_git(work, "commit", "-q", "-m", "other")
        head_same = portable_git(work, "rev-parse", "HEAD", output=True)
        expect_binding(work, synchronize_event(head, head_same, base), head_same, False,
                       "local predecessor lost to the base merge-base")
        expect_binding(work, {"action": "opened"}, head, False, "opened event consulted synchronize state")
        expect_binding_error(work, synchronize_event(head, head, base), head,
                             "missing or identical synchronize context", "identical synchronize context accepted")


def assert_moved_base_merge():
    import tempfile
    with tempfile.TemporaryDirectory() as directory:
        directory = pathlib.Path(directory)
        def g(*args, output=False):
            result = subprocess.run(["git", "-C", str(directory), *args], check=not output,
                                    stdout=subprocess.PIPE if output else subprocess.DEVNULL, text=output)
            return result.stdout.strip() if output else None
        g("init", "-q", "-b", "main")
        g("config", "user.email", "trace@example.com")
        g("config", "user.name", "trace")
        (directory / "file").write_text("base\n")
        g("add", "file")
        g("commit", "-q", "-m", "base")
        base = g("rev-parse", "HEAD", output=True)
        (directory / "later").write_text("later\n")
        g("add", "later")
        g("commit", "-q", "-m", "later")
        tip = g("rev-parse", "HEAD", output=True)
        g("checkout", "-q", "-b", "topic", base)
        (directory / "file").write_text("head\n")
        g("commit", "-q", "-am", "head")
        head = g("rev-parse", "HEAD", output=True)
        g("checkout", "-q", "main")
        g("merge", "-q", "--no-ff", "-m", "merge", "topic")
        merge = g("rev-parse", "HEAD", output=True)
        remote = directory / ".git" / "refs" / "remotes" / "origin"
        remote.mkdir(parents=True)
        (remote / "main").write_text(tip + "\n")
        assert_test_merge(directory, merge, base, head, "main")
        assert_candidate_tree(directory, merge, head)
        try:
            assert_test_merge(directory, merge, base, base, "main")
        except ValueError as error:
            if str(error) != MERGE_PARENTS:
                raise
        else:
            raise AssertionError("wrong head parent accepted")


def main():
    if sys.argv[1:] == ["selftest"]:
        return selftest()
    if sys.argv[1:] == ["run"]:
        return run()
    if len(sys.argv) in (4, 6) and sys.argv[1] == "sink":
        private = [int(value) for value in sys.argv[4:]]
        return sink(sys.argv[2], sys.argv[3], *private)
    raise ValueError("only fixed run/sink/selftest modes")


if __name__ == "__main__":
    sys.exit(main())

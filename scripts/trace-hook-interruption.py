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

PARENT = "406c8859cd00bd30b2339c42af24977791456b85"
WORKFLOW_SHA = "907ab8596d7ac054eb565a8bfbb329464eb3f59e4c77a58e7f5fa17bb0e428db"
CONTROLS_SHA = "3911bfeb428227f21fb06120c25444d6339aa7481fcf295fd31361e26192166d"
SOURCE_PATHS = (".github/workflows/ci-tests.yml", "scripts/trace-hook-interruption.py",
                "scripts/testdata/hook-interruption-trace-controls.py")
ORIGINALS = {
    "scripts/pre_commit_hook_preflight_timeout_test.go": "99aaac8055bc75428ecc7744946832096822163315ae5676007f77ee3d1c4777",
    "scripts/pre_commit_hook_test.go": "a6765cfefed6b6ff6b612742227e5c8fb60c4e3eb5f23ef0f76d36ee90418317",
    "scripts/hook-config-preflight.sh": "0cb26b26b33ab467788261145c02a0a04c536e4bfcf2f9444d86357ccb668c82",
    "Makefile": "300d4b99ec808eeb4619b701527b5e10822cdfcd1703221170eb5b53415de971",
}
SYSCALLS = ("clone,clone3,fork,vfork,execve,execveat,exit,exit_group,wait4,waitid,"
            "setpgid,getpgid,getpgrp,setsid,getsid,kill,tkill,tgkill,rt_sigaction,"
            "rt_sigprocmask,rt_sigreturn,pipe,pipe2,dup,dup2,dup3,fcntl,close,"
            "close_range,open,openat,openat2,read,readv,write,writev")
SELECTOR = "^TestHooksInstallInterruptCleansPreflightAndRollsBackState$"
STREAMS = ("exit0", "exit23", "signal", "groups", "cap", "write-error",
           "missing", "abnormal", "cancel", "focused")
CAPS = {name: (8388608 if name in ("cap", "focused") else 65536) for name in STREAMS}
SLOTS = {name + TRACE_SUFFIX: CAPS[name] for name in STREAMS}
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
                    raise OSError("zero write")
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
                raise OSError("zero write")
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
    if slot not in STREAMS:
        raise ValueError("invalid fixed stream")
    descriptor = owned_directory(directory)
    try:
        fd = os.open(slot + TRACE_SUFFIX, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
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
    waited = re.search(rb"wait4\([^\n]*\)\s+=\s+(0x[\da-f]+|\d+)\s", line, re.ASCII)
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


def git(root, *arguments):
    env = dict(os.environ)
    for key in tuple(env):
        if key.startswith("GIT_"):
            del env[key]
    env["GIT_NO_REPLACE_OBJECTS"] = "1"
    env["GIT_OPTIONAL_LOCKS"] = "0"
    process = subprocess.Popen(["git", "--no-pager", "-c", "core.fsmonitor=false",
                                "-c", "core.hooksPath=/dev/null", "-C", str(root), *arguments], env=env, stdout=subprocess.PIPE)
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
        if not re.fullmatch("[0-9a-f]{40}", value):
            raise ValueError("missing immutable caller context")
    if git(root, "rev-parse", "HEAD").decode().strip() != source or env.get("GITHUB_SHA") != source:
        raise ValueError("checkout/source mismatch")
    if git(root, "show", "-s", "--format=%P", source).decode().split() != [base, head]:
        raise ValueError("checkout not actual base/head merge")
    if git(root, "show", "-s", "--format=%P", head).decode().split() != [PARENT]:
        raise ValueError("candidate parent differs from reviewed parent")
    # This bounded candidate admits a base already contained in the selected
    # parent. An advanced base needs a fresh source binding/review, not a merge
    # reconstruction fallback or new object writes in this diagnostic.
    git(root, "merge-base", "--is-ancestor", base, PARENT)
    if git(root, "rev-parse", source + TREE_SUFFIX) != git(root, "rev-parse", head + TREE_SUFFIX):
        raise ValueError("merge contains source outside the exact candidate tree")
    git(root, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "HEAD")
    if git(root, "ls-files", "--others", "--", "*.go", "go.mod", "go.sum").strip():
        raise ValueError("untracked compile inputs")
    return event, head, base, source


def candidate_contents(root, head, source):
    changed = git(root, "diff", "--name-only", "-z", PARENT, head).decode().strip("\0").split("\0")
    if set(changed) != set(SOURCE_PATHS) or len(changed) != 3:
        raise ValueError("empty or out-of-scope candidate delta")
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
    normalized = workflow[:match.start(1)] + b"0" * 64 + workflow[match.end(1):]
    if digest(normalized) != WORKFLOW_SHA or file_identity(root / SOURCE_PATHS[2])["sha256"] != CONTROLS_SHA:
        raise ValueError("reviewed source binding mismatch")


def original_binding(root, head):
    for name, expected in ORIGINALS.items():
        for contents in (bounded_read(root / name, 1048576), git(root, "show", head + ":" + name), git(root, "show", PARENT + ":" + name)):
            if digest(contents) != expected:
                raise ValueError("original source identity changed")
    installer = bounded_read(root / "Makefile", 1048576).split(b"hooks-install:\n", 1)[1].split(b"\nvscode-extension-install:", 1)[0]
    if digest(installer) != "71e7577dd31f8597f5a7ced6d0633063b156f7ece322f49a10c6cb045964cbf3":
        raise ValueError("installer extraction changed")


def synchronize_binding(root, event, head):
    if event["action"] == "synchronize":
        before = event.get("before")
        if not isinstance(before, str) or not re.fullmatch("[0-9a-f]{40}", before) or before == head or event.get("after") != head:
            raise ValueError("missing or identical synchronize context")
        if not git(root, "diff", "--name-only", before, head, "--", *SOURCE_PATHS).strip():
            raise ValueError("same-source commit churn")


def validate_binding(root):
    event, head, base, source = event_context(root)
    candidate_contents(root, head, source)
    diagnostic_binding(root)
    original_binding(root, head)
    synchronize_binding(root, event, head)
    return {"parent": PARENT, "head": head, "base": base, "checkout_merge": source,
            "checkout_tree": git(root, "rev-parse", source + TREE_SUFFIX).decode().strip(),
            "action": event["action"], "originals": ORIGINALS,
            "delta_sha256": digest(git(root, "diff", "--binary", "--no-ext-diff", "--no-textconv", PARENT, head)),
            "review_authority_claim": False}


def tracer_identity(result):
    tracer = file_identity(STRACE)
    result["preflight"].update({"strace": tracer, "image": os.environ.get("ImageVersion", "")[:256],
                                "kernel": platform.release(), "architecture": platform.machine()})
    if os.environ.get("ImageVersion") != "20261004.327.1" or tracer["sha256"] != "28f957c227012de0b18d1bd7fff2d396cb693ea60ed8013be68de071e84b5001":
        raise ValueError("unreviewed image or tracer")
    if tracer["uid"] != 0 or tracer["mode"] & 0o022:
        raise ValueError("tracer ownership/mode")
    version = subprocess.check_output([STRACE, "-V"], stderr=subprocess.STDOUT)
    package = subprocess.check_output(["/usr/bin/dpkg-query", "-W", "-f=${Version} ${Architecture}", "strace"])
    result["preflight"].update({"strace_version": version[:4096].decode(errors="replace"), "dpkg": package[:256].decode(errors="replace")})
    if len(version) > 4096 or b"version 6.8" not in version or package != b"6.8-0ubuntu2 amd64":
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


def compile_normal(root, build, result):
    go = file_identity(shutil.which("go"))
    if pathlib.Path(go["realpath"]).is_relative_to(root) or go["mode"] & 0o022:
        raise ValueError("compiler executable custody")
    if os.environ.get("GOFLAGS") or os.environ.get("GOEXPERIMENT"):
        raise ValueError("unreviewed compile flags")
    # Verify the installed runtime with local toolchain selection before using
    # the unchanged fixed version. No toolchain/module download fallback.
    env = dict(os.environ, GOTOOLCHAIN="local", GOENV="off", GOPROXY="off", GOSUMDB="off")
    result["go_executable"] = go
    go_version = subprocess.check_output([go["realpath"], "version"], env=env)
    result["go_version"] = go_version[:4096].decode(errors="replace")
    if not go_version.startswith(b"go version go1.27.2 "):
        raise ValueError("compiler version")
    env["GOTOOLCHAIN"] = "go1.27.2"
    goroot = subprocess.check_output([go["realpath"], "env", "GOROOT"], env=env).decode().strip()
    result["go_executable"] = go
    result["compiler"] = file_identity(pathlib.Path(goroot) / "pkg/tool/linux_amd64/compile")
    result["go_version"] = go_version.decode()
    binary = build / "hook.test"
    compile_status = subprocess.call([go["realpath"], "test", "-c", "-ldflags", "-X github.com/ben-ranford/lopper/internal/version.buildChannel=dev", "-o", str(binary), "./scripts"], cwd=root, env=env)
    result["compile_status"] = compile_status
    if compile_status:
        raise ValueError("normal compile failed")
    result["binary"] = file_identity(binary)
    buildinfo = subprocess.check_output([go["realpath"], "version", "-m", str(binary)], env=env)
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
        (directory / "exit23.trace").write_bytes(b"12 1.000000 wait4(0xd, 0x1234, 0x0, 0x0) = 0xd <0.01>\n12 1.000001 +++ exited with 23 +++\n")
        facts = raw_facts(directory, "exit23", 64)
        assert facts["raw_wait4_facts"] == [{"returned_pid_numeric": 13, "wait_status_pointer_contents": "OPAQUE"}]
        assert facts["terminal_facts"] == [{"pid_numeric": 12, "terminal": "exited with 23"}]
        assert facts["continuity"] == "UNKNOWN"
    assert len(SLOTS) == 23 and sum(SLOTS.values()) == MAX_RETAINED
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
    print("portable support checks PASS; native controls NOT executed")
    return 0


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

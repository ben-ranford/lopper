#!/usr/bin/python3
"""Six fixed private native controls; selftest is portable supporting evidence only."""
import array
import fcntl
import json
import os
import re
import stat
import signal
import socket
import sys
import time

if len(sys.argv) < 2 or sys.argv[1] not in ("target", "sentinel"):
    import pathlib
    import runpy
    import select
    import subprocess

WORKSPACE_ERROR = "privacy workspace custody"
PRIVACY_PREFIX = "HOOK_TRACE_PRIVACY_SENTINEL_"
PYTHON = "/usr/bin/python3"

DRIVER = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "trace-hook-interruption.py")


def send(channel, value, fd=None):
    payload = json.dumps(value, separators=(",", ":")).encode()
    if len(payload) > 4096:
        raise ValueError("witness bound")
    ancillary = [] if fd is None else [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [fd]))]
    channel.sendmsg([payload], ancillary)


def private_workspace(workspace):
    # Target mode keeps its narrow imports; no driver/module execution in tracee.
    temp = os.path.realpath(os.environ["RUNNER_TEMP"])
    identifiers = [os.environ[name] for name in ("GITHUB_RUN_ID", "GITHUB_JOB", "GITHUB_RUN_ATTEMPT")]
    if any(re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value, re.ASCII) is None for value in identifiers):
        raise ValueError(WORKSPACE_ERROR)
    name = "hook-native-" + "-".join(identifiers) + "-build"
    if os.fspath(workspace) != os.path.join(temp, name):
        raise ValueError(WORKSPACE_ERROR)
    expected_parent = os.stat(temp)
    parent = os.open(temp, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    descriptor = None
    try:
        info = os.fstat(parent)
        expected = os.stat(name, dir_fd=parent, follow_symlinks=False)
        private_parent = info.st_uid == os.getuid() and not info.st_mode & 0o022
        sticky_temp = info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o1777
        if not (private_parent or sticky_temp) or (info.st_dev, info.st_ino) != (expected_parent.st_dev, expected_parent.st_ino):
            raise ValueError(WORKSPACE_ERROR)
        if not stat.S_ISDIR(expected.st_mode) or expected.st_uid != os.getuid() or stat.S_IMODE(expected.st_mode) != 0o700:
            raise ValueError(WORKSPACE_ERROR)
        descriptor = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        actual = os.fstat(descriptor)
        current = os.stat(name, dir_fd=parent, follow_symlinks=False)
        if (actual.st_dev, actual.st_ino, actual.st_uid, actual.st_mode) != (expected.st_dev, expected.st_ino, expected.st_uid, expected.st_mode) or (current.st_dev, current.st_ino) != (actual.st_dev, actual.st_ino):
            raise ValueError(WORKSPACE_ERROR)
        result = descriptor
        descriptor = None
        return result
    finally:
        if descriptor is not None:
            os.close(descriptor)
        os.close(parent)


def privacy_target(channel, slot, workspace):
    sentinel = sys.argv[-1]
    if slot not in ("exit0", "exit23", "signal") or re.fullmatch(PRIVACY_PREFIX + slot + r"_[\da-f]{32}", sentinel, re.ASCII) is None:
        raise ValueError("private sentinel identity")
    # Real argv/env/path/read/write sentinels, never synthetic trace strings.
    if sys.argv[-1] != sentinel or os.environ.get("HOOK_TRACE_PRIVATE_SENTINEL") != sentinel:
        raise ValueError("privacy fixture inputs")
    descriptor = private_workspace(workspace)
    try:
        fd = os.open(sentinel, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        with os.fdopen(fd, "wb", buffering=0) as stream:
            stream.write(sentinel.encode())
        fd = os.open(sentinel, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=descriptor)
        with os.fdopen(fd, "rb", buffering=0) as stream:
            if stream.read(4096) != sentinel.encode():
                raise ValueError("privacy fixture IO")
    finally:
        os.close(descriptor)
    read_fd, write_fd = os.pipe()
    os.write(write_fd, sentinel.encode())
    if os.read(read_fd, 4096) != sentinel.encode():
        raise ValueError("privacy pipe")
    os.close(read_fd)
    os.close(write_fd)
    send(channel, {"privacy_io": True})
    if slot == "signal":
        os.kill(os.getpid(), signal.SIGTERM)
    return 23 if slot == "exit23" else 0


def group_holder(channel, read_fd, write_fd):
    os.setsid()
    os.close(read_fd)
    # FD3 may be the control socket: move it first, then explicitly dup.
    moved = socket.socket(fileno=fcntl.fcntl(channel.fileno(), fcntl.F_DUPFD_CLOEXEC, 64))
    channel.close()
    os.dup2(write_fd, 3)
    if write_fd != 3:
        os.close(write_fd)
    send(moved, {"holder": os.getpid(), "pgid": os.getpgrp(), "sid": os.getsid(0), "write_fd": 3})
    os.write(3, b"X")
    if moved.recv(16) != b"release":
        os._exit(24)
    os.close(3)
    moved.close()
    os._exit(0)


def groups_target(channel):
    os.setpgid(0, 0)
    waited = os.fork()
    if waited == 0:
        os._exit(23)
    child, wait_status = os.waitpid(waited, 0)
    if child != waited or not os.WIFEXITED(wait_status) or os.WEXITSTATUS(wait_status) != 23:
        raise ValueError("independent real wait4 status")
    send(channel, {"waited_pid": child, "wait_status": wait_status, "exit_status": 23})
    read_fd, write_fd = os.pipe()
    holder = os.fork()
    if holder == 0:
        group_holder(channel, read_fd, write_fd)
    os.close(write_fd)
    send(channel, {"read_fd_independent": read_fd, "holder_created": holder}, read_fd)
    os.close(read_fd)
    return 0  # Owner retains child writer after this root exit.


def target(slot, descriptor, workspace):
    """Predetermined fixtures only; no external commands, arbitrary files or flags."""
    channel = socket.socket(fileno=descriptor)
    send(channel, {"root": os.getpid(), "pgid": os.getpgrp(), "sid": os.getsid(0)})
    if channel.recv(16) != b"go":
        raise ValueError("owner pidfd admission handshake")
    if slot in ("exit0", "exit23", "signal"):
        return privacy_target(channel, slot, workspace)
    if slot == "groups":
        return groups_target(channel)
    if slot in ("cap", "write-error", "missing", "abnormal"):
        fd = os.open("/dev/null", os.O_WRONLY)
        count = 150000 if slot == "cap" else 128
        for _ in range(count):
            os.write(fd, b"C")
        os.close(fd)
        return 0
    if slot == "cancel":
        while True:
            time.sleep(0.05)
    raise ValueError("invalid target fixture")


class Owner:
    """Own only explicitly launched private control roots and witnessed pidfds."""
    def __init__(self, api, output, workspace):
        self.api = api
        self.output = pathlib.Path(output)
        self.workspace = pathlib.Path(workspace)
        self.total_deadline = time.monotonic() + 30
        self.deadline = self.total_deadline
        self.pids = {}
        self.tracers = []
        self.truth = []
        self.sentinels = {}
        self.sentinel = subprocess.Popen([PYTHON, "-I", "-S", __file__, "sentinel"],
                                         start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def remaining(self, reserve=0):
        remaining = min(self.deadline, self.total_deadline) - time.monotonic() - reserve
        if remaining <= 0:
            raise TimeoutError("fixed private owner deadline")
        return remaining

    def alive(self, descriptor):
        return not select.select([descriptor], [], [], 0)[0]

    def remember(self, pid):
        if type(pid) is not int or pid <= 0 or pid == os.getpid():
            raise ValueError("private PID witness")
        if pid not in self.pids:
            self.pids[pid] = os.pidfd_open(pid)
        return self.pids[pid]

    def receive(self, channel):
        channel.settimeout(self.remaining(0.5))
        payload, ancillary, flags, _ = channel.recvmsg(4097, socket.CMSG_SPACE(array.array("i").itemsize))
        if flags or len(payload) > 4096:
            raise ValueError("witness overflow")
        descriptors = []
        for level, kind, data in ancillary:
            if level != socket.SOL_SOCKET or kind != socket.SCM_RIGHTS:
                raise ValueError("witness ancillary")
            values = array.array("i")
            values.frombytes(data)
            descriptors.extend(values)
        try:
            value = json.loads(payload)
            if type(value) is int:
                value = {"sink": value}
        except ValueError:
            # Fixed abnormal sink witness is its actual PID after checked receipt.
            value = {"sink": int(payload)}
        return value, descriptors

    def launch(self, slot):
        self.remaining(0.5)
        parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        sink_parent = sink_child = None
        target_argv = [PYTHON, "-I", "-S", str(pathlib.Path(__file__).resolve()),
                       "target", slot, str(child.fileno()), str(self.workspace)]
        env = dict(os.environ)
        if slot in ("exit0", "exit23", "signal"):
            sentinel = PRIVACY_PREFIX + slot + "_" + os.urandom(16).hex()
            self.sentinels[slot] = sentinel
            target_argv.append(sentinel)
            env["HOOK_TRACE_PRIVATE_SENTINEL"] = sentinel
        descriptors = [child.fileno()]
        seam = []
        if slot == "abnormal":
            sink_parent, sink_child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
            descriptors.append(sink_child.fileno())
            seam = [sink_child.fileno(), sink_child.fileno()]
        command = self.api["trace_command"](DRIVER, self.output, slot, target_argv, *seam)
        tracer = subprocess.Popen(command, pass_fds=descriptors, env=env, start_new_session=True,
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.tracers.append(tracer)
        child.close()
        if sink_child is not None:
            sink_child.close()
        first, fds = self.receive(parent)
        if fds or "root" not in first:
            raise ValueError("missing root witness")
        self.remember(first["root"])
        parent.send(b"go")
        return tracer, parent, sink_parent, first

    def waited(self, slot, tracer, root):
        code = tracer.wait(timeout=self.remaining(0.5))
        result = self.api["observe"](self.output, slot, code)
        result["raw_facts"] = self.api["raw_facts"](self.output, slot, 64)
        result["control_root_pid_independent"] = root["root"]
        self.truth.append({"slot": slot, "observation": result})
        return code, result

    def assert_status_capture(self, slot, expected, root, code, result):
        trace = self.api["bounded_read"](self.output / (slot + ".trace"), 65536)
        if code != expected or result["sink_receipt_state"] != "COMPLETE":
            raise ValueError("normal/nonzero/signal mirroring")
        terminal = "killed by SIGTERM" if slot == "signal" else "exited with " + str(expected)
        if not any(fact["pid_numeric"] == root["root"] and fact["terminal"] == terminal for fact in result["raw_facts"]["terminal_facts"]):
            raise ValueError("root terminal fact does not match independently admitted root")
        if self.sentinels[slot].encode() in trace or PRIVACY_PREFIX.encode() in trace:
            raise ValueError("raw argv/env/path/read/write privacy leak")

    def status_privacy(self):
        for slot, expected in (("exit0", 0), ("exit23", 23), ("signal", -15)):
            tracer, channel, _, root = self.launch(slot)
            try:
                witness, fds = self.receive(channel)
                if fds or witness != {"privacy_io": True}:
                    raise ValueError("missing real privacy witness")
                code, result = self.waited(slot, tracer, root)
                self.assert_status_capture(slot, expected, root, code, result)
                self.truth[-1]["independent_privacy_io"] = True
            finally:
                channel.close()

    def group_witnesses(self, channel):
        witnesses = []
        read_fd = None
        holder = None
        for _ in range(3):
            value, fds = self.receive(channel)
            witnesses.append(value)
            if fds:
                if len(fds) != 1 or read_fd is not None:
                    raise ValueError("pipe endpoint witness")
                read_fd = fds[0]
            if "holder" in value:
                holder = value
                self.remember(holder["holder"])
        return witnesses, read_fd, holder

    def assert_group_trace(self, code, result, witnesses):
        trace = self.api["bounded_read"](self.output / "groups.trace", 65536)
        if code != 0 or result["sink_receipt_state"] != "COMPLETE":
            raise ValueError("group fixture status/receipt")
        for token in (b"setpgid(", b"setsid(", b"dup2(", b"wait4(", b"close(", b"pipe"):
            if token not in trace:
                raise ValueError("missing actual numeric group/FD/wait syscall")
        waited_pid = next(value["waited_pid"] for value in witnesses if "waited_pid" in value)
        if not any(fact["returned_pid_numeric"] == waited_pid for fact in result["raw_facts"]["raw_wait4_facts"]):
            raise ValueError("actual raw wait4 returned PID fact absent")
        if not re_search_fd3(trace):
            raise ValueError("actual raw FD3 duplication absent")

    def groups_pipe_wait(self):
        tracer, channel, _, root = self.launch("groups")
        read_fd = None
        witnesses = []
        try:
            witnesses, read_fd, holder = self.group_witnesses(channel)
            if read_fd is None or holder["pgid"] != holder["holder"] or holder["sid"] != holder["holder"]:
                raise ValueError("actual private setsid/group witness")
            if not any(value.get("exit_status") == 23 for value in witnesses):
                raise ValueError("independent wait4 fixture status absent")
            os.set_blocking(read_fd, False)
            if not select.select([self.pids[root["root"]]], [], [], self.remaining(0.5))[0]:
                raise ValueError("root did not terminate")
            if not self.alive(self.pids[holder["holder"]]) or tracer.poll() is not None:
                raise ValueError("retained child writer/tracer independence")
            if not select.select([read_fd], [], [], self.remaining(0.5))[0] or os.read(read_fd, 1) != b"X" or select.select([read_fd], [], [], 0)[0]:
                raise ValueError("real pipe incorrectly reports EOF while writer lives")
            channel.send(b"release")
            if not select.select([read_fd], [], [], self.remaining(0.5))[0] or os.read(read_fd, 1) != b"":
                raise ValueError("actual released writer EOF")
            code, result = self.waited("groups", tracer, root)
            self.assert_group_trace(code, result, witnesses)
            self.truth[-1]["independent_fixture_truth"] = witnesses
            self.truth[-1]["pipe_eof_after_owner_release"] = True
        finally:
            channel.close()
            if read_fd is not None:
                os.close(read_fd)

    def cap(self):
        tracer, channel, _, root = self.launch("cap")
        try:
            code, result = self.waited("cap", tracer, root)
            receipt = result.get("receipt", {})
            if code != 0 or result["sink_receipt_state"] != "INCOMPLETE" or not receipt.get("eof"):
                raise ValueError("cap control outcome/drain")
            if receipt.get("retained") != 8388608 or receipt.get("discarded", 0) <= 0 or receipt.get("errors"):
                raise ValueError("actual cap not reached or drain IO failure")
        finally:
            channel.close()

    def write_failure(self):
        tracer, channel, _, root = self.launch("write-error")
        try:
            code, result = self.waited("write-error", tracer, root)
            receipt = result.get("receipt", {})
            if code != 0 or result["sink_receipt_state"] != "FAILED_IO" or not receipt.get("eof"):
                raise ValueError("real /dev/full failed drain")
            if "WRITE_ERROR" not in receipt.get("errors", []) or receipt.get("discarded", 0) <= 0:
                raise ValueError("real write error not witnessed")
        finally:
            channel.close()

    def ambiguity(self):
        tracer, channel, _, root = self.launch("missing")
        try:
            code, result = self.waited("missing", tracer, root)
            if code != 0 or result["sink_receipt_state"] != "MISSING" or result["sink_wait_status"] != "UNOBSERVABLE":
                raise ValueError("missing receipt rejection")
        finally:
            channel.close()
        tracer, channel, witness, root = self.launch("abnormal")
        try:
            value, descriptors = self.receive(witness)
            if descriptors or set(value) != {"sink"}:
                raise ValueError("abnormal witness")
            sink_pidfd = self.remember(value["sink"])
            # Independently confirm the checked receipt exists BEFORE real SIGKILL.
            receipt = json.loads(self.api["bounded_read"](self.output / "abnormal.receipt.json", 4096))
            if receipt["state"] != "COMPLETE" or not receipt["eof"] or not self.alive(sink_pidfd):
                raise ValueError("post-receipt live sink witness")
            signal.pidfd_send_signal(sink_pidfd, signal.SIGKILL)
            if not select.select([sink_pidfd], [], [], self.remaining(0.5))[0]:
                raise ValueError("owned sink abnormal termination not observed")
            code, result = self.waited("abnormal", tracer, root)
            if code != 0 or result["sink_wait_status"] != "UNOBSERVABLE" or result["sink_receipt_state"] != "COMPLETE":
                raise ValueError("post-receipt ambiguity falsified")
            self.truth[-1]["independent_witness"] = {"pidfd_SIGKILL_accepted": True, "pidfd_terminal": True,
                                                      "nonchild_wait_status": "UNOBSERVABLE"}
        finally:
            channel.close()
            witness.close()

    def cancellation(self):
        tracer, channel, _, root = self.launch("cancel")
        try:
            if self.sentinel.poll() is not None:
                raise ValueError("unrelated sentinel disturbed before cancellation")
            tracer.send_signal(signal.SIGTERM)
            # Clean only the actually witnessed private root, if detach leaves it alive.
            root_fd = self.pids[root["root"]]
            if self.alive(root_fd):
                signal.pidfd_send_signal(root_fd, signal.SIGTERM)
            code, result = self.waited("cancel", tracer, root)
            if code == 0 or self.sentinel.poll() is not None:
                raise ValueError("cancellation/isolation evidence")
            self.truth[-1]["cancellation_state"] = "PARTIAL_UNKNOWN"
            self.truth[-1]["unrelated_sentinel_alive"] = True
            result["root_terminal"] = "UNKNOWN"
        finally:
            channel.close()

    def cleanup(self):
        # Only pidfds retained from actual fixed private witnesses; no PID scan/reuse.
        errors = []
        for descriptor in self.pids.values():
            try:
                if self.alive(descriptor):
                    signal.pidfd_send_signal(descriptor, signal.SIGKILL)
                if not select.select([descriptor], [], [], max(0, min(self.deadline, self.total_deadline) - time.monotonic()))[0]:
                    errors.append("UNTERMINATED_PRIVATE_PIDFD")
                os.close(descriptor)
            except OSError as error:
                errors.append(type(error).__name__)
        for tracer in self.tracers:
            if tracer.poll() is None:
                tracer.kill()
            try:
                tracer.wait(timeout=max(0.001, min(self.deadline, self.total_deadline) - time.monotonic()))
            except subprocess.TimeoutExpired:
                errors.append("UNJOINED_TRACER")
        self.pids.clear()
        self.tracers.clear()
        if errors:
            raise ValueError("private cleanup incomplete: " + ",".join(errors))

    def execute(self):
        phases = (self.status_privacy, self.groups_pipe_wait, self.cap, self.write_failure,
                  self.ambiguity, self.cancellation)
        try:
            for phase in phases:
                self.deadline = min(self.total_deadline, time.monotonic() + 5)
                started = time.monotonic()
                phase()
                self.cleanup()
                if self.sentinel.poll() is not None:
                    raise ValueError("unrelated sentinel disturbed")
                self.remaining()
                self.truth.append({"invocation": phase.__name__, "elapsed_seconds": time.monotonic() - started})
            if len([entry for entry in self.truth if "slot" in entry]) != 9:
                raise ValueError("fixed nine stream schedule")
            return 0
        finally:
            try:
                self.cleanup()
            finally:
                self.sentinel.terminate()
                code = self.sentinel.wait(timeout=max(0.001, min(self.deadline, self.total_deadline) - time.monotonic()))
                self.truth.append({"unrelated_sentinel_direct_wait": {"kind": "SIGNAL" if code < 0 else "EXIT", "value": abs(code)}})
                self.remaining()


def controls(output, workspace):
    if sys.platform != "linux" or not hasattr(os, "pidfd_open") or not hasattr(signal, "pidfd_send_signal"):
        raise ValueError("native pidfd witnesses unavailable")
    api = runpy.run_path(str(DRIVER), run_name="native_trace_support")
    owner = Owner(api, output, workspace)
    status = 1
    failure = None
    try:
        status = owner.execute()
    except Exception as error:
        failure = {"kind": type(error).__name__, "reason": str(error)[:512]}
    data = json.dumps({"native_controls_status": status, "truth": owner.truth, "failure": failure}, separators=(",", ":")).encode()
    if len(data) > 16384:
        raise ValueError("private control metadata bound")
    path = pathlib.Path(workspace) / "control-truth.json"
    with path.open("xb", buffering=0) as stream:
        stream.write(data)
    return status


def re_search_fd3(trace):
    import re
    return re.search(rb"dup2\(0x[0-9a-f]+, 0x3\)\s+=\s+0x3", trace) is not None


def selftest():
    api = runpy.run_path(str(DRIVER), run_name="portable_trace_support")
    assert len(api["STREAMS"]) == 10
    assert sum(api["SLOTS"].values()) == 17473536
    expected_names = ("exit0", "exit23", "signal", "groups", "cap", "write-error",
                      "missing", "abnormal", "cancel", "focused")
    expected_traces = {
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
    expected_caps = {name: (8388608 if name in ("cap", "focused") else 65536)
                     for name in expected_names}
    expected_slots = {expected_traces[name]: expected_caps[name] for name in expected_names}
    expected_slots.update({name + ".receipt.json": 4096 for name in expected_names})
    expected_slots.update({"main.json": 32768, "manifest.json": 32768, "preflight.log": 65536})
    assert api["STREAMS"] == expected_names and tuple(api["TRACE_NAMES"]) == expected_names
    assert api["TRACE_NAMES"] == expected_traces
    assert {name: name + api["TRACE_SUFFIX"] for name in expected_names} == expected_traces
    assert api["CAPS"] == expected_caps and api["SLOTS"] == expected_slots
    assert len(api["SLOTS"]) == 23 and api["MAX_RETAINED"] == 17473536
    import tempfile
    with tempfile.TemporaryDirectory() as runner_temp:
        canonical_runner_temp = pathlib.Path(runner_temp).resolve(strict=True)
        run_dir = canonical_runner_temp / "hook-native-g043-selftest-job-1"
        run_dir.mkdir(mode=0o700)
        outside_trace = canonical_runner_temp / "outside.trace"
        outside_receipt = canonical_runner_temp / "outside.receipt.json"
        saved_environment = {name: os.environ.get(name) for name in
                             ("RUNNER_TEMP", "GITHUB_RUN_ID", "GITHUB_JOB", "GITHUB_RUN_ATTEMPT")}
        os.environ.update({"RUNNER_TEMP": runner_temp, "GITHUB_RUN_ID": "g043-selftest",
                           "GITHUB_JOB": "job", "GITHUB_RUN_ATTEMPT": "1"})
        try:
            try:
                api["sink"](run_dir, "../outside")
            except ValueError as error:
                assert str(error) == "invalid fixed stream"
            else:
                raise AssertionError("traversal-shaped sink label accepted")
            assert list(run_dir.iterdir()) == []
            assert not outside_trace.exists() and not outside_receipt.exists()

            sink_globals = api["sink"].__globals__
            original_drain = sink_globals["drain"]
            drain_calls = []
            def fixture_drain(source, _target, cap):
                drain_calls.append((source, cap))
                return {"received": 0, "retained": 0, "discarded": 0,
                        "counter_saturated": False, "eof": True, "errors": []}
            sink_globals["drain"] = fixture_drain
            try:
                assert api["sink"](run_dir, "exit0") == 0
            finally:
                sink_globals["drain"] = original_drain
            assert drain_calls == [(0, expected_caps["exit0"])]
            assert {path.name for path in run_dir.iterdir()} == {"exit0.trace", "exit0.receipt.json"}
            assert not outside_trace.exists() and not outside_receipt.exists()
        finally:
            for name, value in saved_environment.items():
                if value is None:
                    os.environ.pop(name, None)
                else:
                    os.environ[name] = value
    command = api["trace_command"](DRIVER, pathlib.Path("/private/output"), "focused",
                                  ["/private/hook.test", "-test.run=" + api["SELECTOR"], "-test.count=1", "-test.timeout=10m"])
    assert command[:5] == ["/usr/bin/strace", "-f", "-I", "2", "-ttt"]
    assert "raw=all" in command and "signal=all" in command and command[-1] == "-test.timeout=10m"
    assert not any(value in command for value in ("--kill-on-exit", "-ff", "-y", "-yy", "-v"))
    left, right = socket.socketpair()
    try:
        send(left, {"portable": "actual bounded IPC only"})
        assert json.loads(right.recv(4096)) == {"portable": "actual bounded IPC only"}
    finally:
        left.close()
        right.close()
    print("portable fixed-contract checks PASS; six native controls NOT executed")
    return 0


def main():
    if sys.argv[1:] == ["selftest"]:
        return selftest()
    if len(sys.argv) == 4 and sys.argv[1] == "controls":
        return controls(sys.argv[2], sys.argv[3])
    if len(sys.argv) in (5, 6) and sys.argv[1] == "target":
        return target(sys.argv[2], int(sys.argv[3]), sys.argv[4])
    if sys.argv[1:] == ["sentinel"]:
        while True:
            time.sleep(1)
    raise ValueError("fixed controls/target/sentinel/selftest modes only")


if __name__ == "__main__":
    sys.exit(main())

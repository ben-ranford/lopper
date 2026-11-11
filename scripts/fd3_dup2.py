"""FD3 dup2 recognition for strace 6.8 and 6.19. Not review authority."""
import re

_UNFINISHED_FD3 = re.compile(rb"dup2\(0x[0-9a-f]+, 0x3 <unfinished \.\.\.>$")
_RESUMED_DUP2 = re.compile(rb"<\.\.\. dup2 resumed>\)\s+=\s+(\S+)")


def syscall_pid_and_body(line):
    if line.startswith(b"[pid "):
        bracket = line.find(b"]", 5)
        pid = line[5:bracket]
        if bracket < 0 or not pid.isdigit():
            return None
        rest = line[bracket + 1:]
    else:
        pid, sep, rest = line.partition(b" ")
        if sep != b" " or not pid.isdigit():
            return None
    timestamp, sep, body = rest.lstrip(b" ").partition(b" ")
    seconds, dot, fraction = timestamp.partition(b".")
    if sep != b" " or dot != b"." or not seconds.isdigit() or not fraction.isdigit():
        return None
    return pid, body


def re_search_fd3(trace):
    # strace 6.8 prints the return on the call line. strace 6.19 splits it:
    # "dup2(0x5, 0x3 <unfinished ...>" then "<... dup2 resumed>) = 0x3".
    # Another task can emit a line between that pair; pair those by PID.
    if re.search(rb"dup2\(0x[0-9a-f]+, 0x3\)\s+=\s+0x3", trace) is not None:
        return True
    if re.search(
        rb"dup2\(0x[0-9a-f]+, 0x3 <unfinished \.\.\.>\n"
        rb"[^\n]*<\.\.\. dup2 resumed>\)\s+=\s+0x3",
        trace,
    ) is not None:
        return True
    return fd3_dup2_resumes_across_lines(trace)


def fd3_resume_state(pending, pid, body):
    if _UNFINISHED_FD3.fullmatch(body):
        pending[pid] = True
        return False
    if body.startswith(b"dup2(") and b"<unfinished ...>" in body:
        pending[pid] = False
        return False
    resumed = _RESUMED_DUP2.match(body)
    if resumed is None:
        return False
    matched = pending.get(pid) is True and resumed.group(1) == b"0x3"
    pending.pop(pid, None)
    return matched


def fd3_dup2_resumes_across_lines(trace):
    pending = {}
    for line in trace.splitlines():
        parsed = syscall_pid_and_body(line)
        if parsed is not None and fd3_resume_state(pending, parsed[0], parsed[1]):
            return True
    return False

#!/usr/bin/env python3
"""Require executed preview and rollback command tests in go test -json output."""
import json
import math
import platform
import sys

REQUIRED = {
    "TestStaveTUIFeatureFlagRendersAndQuitsInPTY",
    "TestStaveTUIResizeAndInterruptExitWithinBound",
    "TestStaveTUIProcessSignalsRestoreTerminal",
    "TestStaveTUIInteractiveNavigationFilterDetailAndHelp",
    "TestTUIWithoutStaveFlagUsesLegacyLinePath",
    "TestStaveTUIDumbTerminalErrorsAndHelpStayVisible",
}
PACKAGE = "github.com/ben-ranford/lopper/cmd/lopper"
UI_PACKAGE = "github.com/ben-ranford/lopper/internal/ui"
PARENT_CANCELLATION_TEST = "TestStavePreviewParentCancellationRestoresTerminalAndReturnsCause"
PARENT_CANCELLATION_REPEATS = 20
PARENT_CANCELLATION_TIMEOUT_SECONDS = 300
RESTORATION_TESTS = {
    "TestStaveRestorationWaitsForReaderAcrossShutdown": {
        "readerCancellation": 15,
        "readerStopped": 15,
        "readerClose": 15,
    },
    "TestStaveRestorationSignalProcess": {"signalHelperSubprocess": 15},
    "TestStaveRestorationBeforeInputStart": {},
}
RESTORATION_SUBTESTS = {
    "TestStaveRestorationWaitsForReaderAcrossShutdown": {
        "quit",
        "parent-cancel",
        "program-error",
        "close-error",
    },
    "TestStaveRestorationSignalProcess": {"SIGINT", "SIGTERM"},
    "TestStaveRestorationBeforeInputStart": set(),
}
RESTORATION_REPEATS = 20
RESTORATION_TIMEOUT_SECONDS = 300


def verify_results(stream):
    passed = set()
    package_passed = False
    for line in stream:
        result = json.loads(line)
        if not isinstance(result, dict):
            raise ValueError("test output must contain JSON objects")
        if result.get("Package") != PACKAGE:
            continue
        if result.get("Action") == "fail":
            raise ValueError("command test failure reported")
        if result.get("Action") == "pass":
            if "Test" in result:
                passed.add(result["Test"])
            else:
                package_passed = True
    missing = REQUIRED - passed
    if missing:
        raise ValueError("required tests did not pass: " + ", ".join(sorted(missing)))
    if not package_passed:
        raise ValueError("command package did not finish successfully")


def elapsed_seconds(result, description):
    elapsed = result.get("Elapsed")
    if isinstance(elapsed, bool) or not isinstance(elapsed, (int, float)):
        raise ValueError(description + " has no numeric elapsed duration")
    try:
        finite = math.isfinite(elapsed)
    except OverflowError:
        finite = False
    if not finite or elapsed < 0:
        raise ValueError(description + " has an invalid elapsed duration")
    return elapsed


def verify_parent_cancellation_timing(stream):
    state = {"runs": 0, "samples": [], "packageElapsed": None, "packagePasses": 0}
    for line in stream:
        record_parent_timing_event(json.loads(line), state)
    validate_parent_timing_state(state)
    print(json.dumps(parent_timing_summary(state), separators=(",", ":")))


def record_parent_timing_event(result, state):
    if not isinstance(result, dict):
        raise ValueError("test output must contain JSON objects")
    if result.get("Package") != UI_PACKAGE:
        return
    action = result.get("Action")
    test_name = result.get("Test")
    if action == "fail":
        raise ValueError("UI package or parent-cancellation test failed")
    if action == "skip":
        raise ValueError("UI package test was skipped")
    if test_name is not None and test_name != PARENT_CANCELLATION_TEST:
        if action in {"run", "pass"}:
            raise ValueError("unexpected UI test appeared in parent-cancellation timing output")
    if test_name == PARENT_CANCELLATION_TEST:
        if action == "run":
            state["runs"] += 1
        elif action == "pass":
            state["samples"].append(elapsed_seconds(result, "parent-cancellation pass"))
    elif test_name is None and action == "pass":
        state["packagePasses"] += 1
        state["packageElapsed"] = elapsed_seconds(result, "UI package completion")


def validate_parent_timing_state(state):
    if state["runs"] != PARENT_CANCELLATION_REPEATS:
        raise ValueError(f"parent-cancellation test ran {state['runs']} times; want {PARENT_CANCELLATION_REPEATS}")
    if len(state["samples"]) != PARENT_CANCELLATION_REPEATS:
        raise ValueError(f"parent-cancellation test passed {len(state['samples'])} times; want {PARENT_CANCELLATION_REPEATS}")
    if state["packagePasses"] != 1 or state["packageElapsed"] is None:
        raise ValueError("UI package did not finish successfully exactly once")


def parent_timing_summary(state):
    host_os = {"Darwin": "macOS", "Linux": "Linux"}.get(platform.system())
    if host_os is None:
        raise ValueError("timing evidence requires a native Linux or macOS runner")
    maximum = max(state["samples"])
    summary = {
        "package": UI_PACKAGE,
        "test": PARENT_CANCELLATION_TEST,
        "hostOS": host_os,
        "repeatCount": len(state["samples"]),
        "successfulElapsedSeconds": state["samples"],
        "maximumTestElapsedSeconds": maximum,
        "packageElapsedSeconds": state["packageElapsed"],
        "packageTimeoutSeconds": PARENT_CANCELLATION_TIMEOUT_SECONDS,
        "packageTimeoutMarginSeconds": PARENT_CANCELLATION_TIMEOUT_SECONDS - state["packageElapsed"],
        "phaseDeadlinesSeconds": {"startupAndReaderControl": 15, "cancellationAcknowledgement": 1, "previewReturn": 1},
    }
    return summary


def verify_restoration_timing(stream):
    state = {
        test_name: {"runs": 0, "samples": [], "packageElapsed": None}
        for test_name in RESTORATION_TESTS
    }
    state["packagePasses"] = 0
    for line in stream:
        record_restoration_timing_event(json.loads(line), state)
    summaries = validate_restoration_timing_state(state)
    host_os = restoration_host_os()
    for summary in summaries:
        summary["hostOS"] = host_os
        print(json.dumps(summary, separators=(",", ":")))


def record_restoration_timing_event(result, state):
    if not isinstance(result, dict):
        raise ValueError("test output must contain JSON objects")
    action = result.get("Action")
    test_name = result.get("Test")
    if result.get("Package") != UI_PACKAGE:
        if test_name is not None or action in {"run", "pass", "fail", "skip"}:
            raise ValueError("unexpected package appeared in restoration timing output")
        return
    if action == "fail":
        raise ValueError("UI package or restoration test failed")
    if action == "skip":
        raise ValueError("UI package or restoration test was skipped")
    if test_name is None:
        if action == "pass":
            state["packagePasses"] += 1
            state["packageElapsed"] = elapsed_seconds(result, "UI package completion")
        return
    root = restoration_test_root(test_name)
    if root is None:
        if action in {"run", "pass", "fail", "skip"}:
            raise ValueError("unexpected UI test appeared in restoration timing output: " + test_name)
        return
    if action == "run" and test_name == root:
        state[root]["runs"] += 1
    elif action == "pass":
        record_restoration_test_event(result, state, root, test_name)


def record_restoration_test_event(result, state, root, test_name):
    elapsed = elapsed_seconds(result, "restoration test pass")
    if test_name == root:
        state[root]["samples"].append(elapsed)


def restoration_test_root(test_name):
    for root, subtests in RESTORATION_SUBTESTS.items():
        if test_name == root or test_name in {root + "/" + name for name in subtests}:
            return root
    return None


def restoration_host_os():
    host_os = {"Darwin": "macOS", "Linux": "Linux"}.get(platform.system())
    if host_os is None:
        raise ValueError("restoration timing evidence requires a native Linux or macOS runner")
    return host_os


def validate_restoration_timing_state(state):
    if state["packagePasses"] != 1 or state["packageElapsed"] is None:
        raise ValueError("UI package did not finish successfully exactly once")
    package_elapsed = state["packageElapsed"]
    if package_elapsed > RESTORATION_TIMEOUT_SECONDS:
        raise ValueError("UI package exceeded the five-minute restoration timing timeout")
    summaries = []
    for test_name, phase_bounds in RESTORATION_TESTS.items():
        result = state[test_name]
        if result["runs"] != RESTORATION_REPEATS:
            raise ValueError(f"{test_name} ran {result['runs']} times; want {RESTORATION_REPEATS}")
        if len(result["samples"]) != RESTORATION_REPEATS:
            raise ValueError(f"{test_name} passed {len(result['samples'])} times; want {RESTORATION_REPEATS}")
        summaries.append(restoration_timing_summary(test_name, result["samples"], package_elapsed, phase_bounds))
    return summaries


def restoration_timing_summary(test_name, samples, package_elapsed, phase_bounds):
    return {
        "package": UI_PACKAGE,
        "test": test_name,
        "repeatCount": len(samples),
        "successfulElapsedSeconds": samples,
        "maximumTestElapsedSeconds": max(samples),
        "packageElapsedSeconds": package_elapsed,
        "packageTimeoutSeconds": RESTORATION_TIMEOUT_SECONDS,
        "packageTimeoutMarginSeconds": RESTORATION_TIMEOUT_SECONDS - package_elapsed,
        "restorationPhaseBoundsSeconds": phase_bounds,
    }


def main():
    if sys.argv[1:] == ["--parent-cancellation-timing"]:
        verify_parent_cancellation_timing(sys.stdin)
    elif sys.argv[1:] == ["--restoration-timing"]:
        verify_restoration_timing(sys.stdin)
    elif sys.argv[1:]:
        raise ValueError("usage: check-stave-test-results.py [--parent-cancellation-timing|--restoration-timing]")
    else:
        verify_results(sys.stdin)
        print(f"Verified all {len(REQUIRED)} preview and legacy command tests executed and passed.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, TypeError) as error:
        sys.exit(str(error))

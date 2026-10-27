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


def main():
    if sys.argv[1:] == ["--parent-cancellation-timing"]:
        verify_parent_cancellation_timing(sys.stdin)
    elif sys.argv[1:]:
        raise ValueError("usage: check-stave-test-results.py [--parent-cancellation-timing]")
    else:
        verify_results(sys.stdin)
        print(f"Verified all {len(REQUIRED)} preview and legacy command tests executed and passed.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, TypeError) as error:
        sys.exit(str(error))

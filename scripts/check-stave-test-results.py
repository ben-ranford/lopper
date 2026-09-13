#!/usr/bin/env python3
"""Require executed preview and rollback command tests in go test -json output."""
import json
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


if __name__ == "__main__":
    try:
        verify_results(sys.stdin)
    except (ValueError, TypeError) as error:
        sys.exit(str(error))
    print(f"Verified all {len(REQUIRED)} preview and legacy command tests executed and passed.")

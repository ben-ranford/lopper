#!/usr/bin/env python3
"""Read-only queue bridge to the protected shared reuse implementation."""

import contextlib
import json
import os
from pathlib import Path
import sys

import reuse_event as shared

MAX_DOCUMENT_BYTES = 1024 * 1024


class ReadOnlyGitHub(shared.GitHub):
    def request(self, path, data=None):
        if data is not None:
            raise shared.EventError("Queue reuse validation cannot mutate GitHub")
        return super().request(path)


def document(stream):
    raw = stream.read(MAX_DOCUMENT_BYTES + 1)
    if len(raw.encode("utf-8")) > MAX_DOCUMENT_BYTES:
        raise shared.EventError("Queue reuse input exceeds its bound")
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise shared.EventError("Queue reuse input must be an object")
    return value


def validate_analysis(result, snapshot):
    shared.validate_result(result, snapshot)
    if result["detector_exit"] != 0:
        raise shared.EventError("Protected reuse detectors did not pass")


def execute(command, value, api, root):
    fields = {"snapshot"} if command == "analyze" else {"snapshot", "analysis", "suppression"}
    if command not in ("analyze", "validate") or set(value) != fields:
        raise shared.EventError("Invalid queue reuse command or schema")
    snapshot = shared.validate_snapshot(value["snapshot"])
    if snapshot["repository"] != "ben-ranford/lopper" or snapshot["base_ref"] != "main":
        raise shared.EventError("Queue reuse must target the protected repository")
    shared.same_live_pair(api, snapshot)
    if command == "analyze":
        # The workflow invokes this only in its separate read-only job. The
        # shared analyzer loads protected tools; candidate source remains data.
        with contextlib.redirect_stdout(sys.stderr):
            result = shared.analyze(api, snapshot, root)
        validate_analysis(result, snapshot)
        return result
    analysis, receipt = value["analysis"], value["suppression"]
    validate_analysis(analysis, snapshot)
    shared.validate_suppression(receipt, snapshot)
    reviews = shared.review_evidence(api, snapshot, analysis, ["ben-ranford"])
    producer = shared.suppression_evidence(api, snapshot, receipt)
    if shared.review_evidence(api, snapshot, analysis, ["ben-ranford"]) != reviews:
        raise shared.EventError("Live reuse review evidence changed")
    if shared.suppression_evidence(api, snapshot, receipt) != producer:
        raise shared.EventError("Live suppression producer evidence changed")
    shared.same_live_pair(api, snapshot)
    return {"reviews": reviews, "producer": producer}


def main(argv=None):
    arguments = sys.argv[1:] if argv is None else argv
    try:
        if len(arguments) != 1:
            raise shared.EventError("Specify one queue reuse command")
        value = document(sys.stdin)
        result = execute(arguments[0], value, ReadOnlyGitHub(os.environ.get("GH_TOKEN")),
                         Path(__file__).resolve().parents[1])
        encoded = json.dumps(result, sort_keys=True, separators=(",", ":"))
        if len(encoded.encode("utf-8")) > MAX_DOCUMENT_BYTES:
            raise shared.EventError("Queue reuse evidence exceeds its bound")
        print(encoded)
        return 0
    except (OSError, ValueError, KeyError, TypeError) as error:
        # Do not echo candidate-controlled text or credentials as commands.
        print("Queue reuse verification failed: " + type(error).__name__, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())

"""Queue bridge failures cannot become a protected reuse approval."""

import copy
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

import queue_me_reuse as bridge


class QueueReuseTests(unittest.TestCase):
    def setUp(self):
        self.snapshot = {"version": 2, "policy_source": "a" * 40, "repository": "ben-ranford/lopper", "repository_id": 1155023607,
                         "head_repository_id": 1155023607, "pull_number": 1772,
                         "base": "a" * 40, "head": "b" * 40, "base_ref": "main"}
        self.analysis = {"version": 2, "snapshot": self.snapshot, "candidate": "c" * 40,
                         "detector_exit": 0, "policy_paths": []}
        self.receipt = {"version": 2, "policySHA": "a" * 40, "headSHA": "b" * 40, "baseSHA": "a" * 40, "runId": 5,
                        "runAttempt": 1, "artifactId": 6, "suppressionCount": 0}
        self.value = {"snapshot": self.snapshot, "analysis": self.analysis, "suppression": self.receipt}
        self.pair = patch.object(bridge.shared, "same_live_pair").start()
        self.reviews = patch.object(bridge.shared, "review_evidence", return_value=[]).start()
        self.producer = patch.object(bridge.shared, "suppression_evidence", return_value={"id": 6}).start()
        self.publish = patch.object(bridge.shared, "publish", side_effect=AssertionError("must never publish")).start()
        self.addCleanup(patch.stopall)

    def execute(self, command="validate", value=None):
        return bridge.execute(command, self.value if value is None else value, object(), Path("/protected"))

    def test_success_repeats_live_reviews_producer_and_pair(self):
        self.assertEqual(self.execute(), {"reviews": [], "producer": {"id": 6}})
        self.assertEqual(self.reviews.call_count, 2)
        self.assertEqual(self.producer.call_count, 2)
        self.assertEqual(self.pair.call_count, 2)
        self.publish.assert_not_called()

    def test_nonzero_and_malformed_analysis_hold(self):
        for change in ({"detector_exit": 1}, {"detector_exit": 2}, {"detector_exit": False},
                       {"snapshot": {}}, {"candidate": "main"}, {"extra": True}):
            with self.subTest(change=change):
                value = copy.deepcopy(self.value)
                value["analysis"].update(change)
                with self.assertRaises((ValueError, KeyError, TypeError)):
                    self.execute(value=value)

    def test_forged_nonzero_or_wrong_pair_receipt_holds(self):
        for change in ({"suppressionCount": 1}, {"suppressionCount": False}, {"headSHA": "c" * 40},
                       {"baseSHA": "d" * 40}, {"artifactId": 0}, {"runId": True}, {"extra": 0}):
            with self.subTest(change=change):
                value = copy.deepcopy(self.value)
                value["suppression"].update(change)
                with self.assertRaises((ValueError, KeyError, TypeError)):
                    self.execute(value=value)

    def test_live_review_or_producer_change_holds(self):
        for target in (self.reviews, self.producer):
            with self.subTest(target=target):
                target.side_effect = [{"id": 1}, {"id": 2}]
                with self.assertRaisesRegex(ValueError, "changed"):
                    self.execute()
                target.side_effect = None

    def test_pair_drift_at_start_or_last_boundary_holds(self):
        for values in ([ValueError("pair drift")], [None, ValueError("pair drift")]):
            self.pair.side_effect = values
            with self.assertRaisesRegex(ValueError, "pair drift"):
                self.execute()

    def test_shared_producer_rejection_is_not_ignored(self):
        self.producer.side_effect = ValueError("newer failed producer")
        with self.assertRaisesRegex(ValueError, "newer failed"):
            self.execute()

    def test_analysis_uses_shared_detector_and_requires_zero(self):
        with patch.object(bridge.shared, "analyze", return_value=self.analysis) as analyze:
            self.assertEqual(self.execute("analyze", {"snapshot": self.snapshot}), self.analysis)
            self.assertEqual(analyze.call_args.args[2], Path("/protected"))
            self.analysis["detector_exit"] = 1
            with self.assertRaisesRegex(ValueError, "did not pass"):
                self.execute("analyze", {"snapshot": self.snapshot})

    def test_invalid_command_input_and_repository_hold(self):
        for command, value in (("publish", self.value), ("analyze", self.value),
                               ("validate", {"snapshot": self.snapshot})):
            with self.assertRaises(ValueError):
                self.execute(command, value)
        self.snapshot["repository"] = "elsewhere/lopper"
        with self.assertRaises(ValueError):
            self.execute()

    def test_bounded_json_input(self):
        for raw in ("[]", "{} " + " " * bridge.MAX_DOCUMENT_BYTES, "{"):
            stream = io.StringIO(raw)
            with self.assertRaises(ValueError):
                bridge.document(stream)

    def invoke_main(self):
        output = io.StringIO()
        with patch.object(bridge.sys, "stdin", io.StringIO(json.dumps(self.value))), \
                patch.object(bridge.sys, "stdout", output), \
                patch.object(bridge.sys, "stderr", io.StringIO()), \
                patch.object(bridge, "ReadOnlyGitHub", return_value=object()):
            status = bridge.main(["validate"])
        return status, output.getvalue()

    def test_typed_ci_deferral_rechecks_pair_and_returns_no_success_evidence(self):
        for producers in ([bridge.shared.CIDeferred("pending")],
                          [{"id": 6}, bridge.shared.CIDeferred("superseded")]):
            with self.subTest(producers=producers):
                self.producer.side_effect = producers
                self.pair.reset_mock()
                status, output = self.invoke_main()
                self.assertEqual(status, 75)
                self.assertEqual(json.loads(output), {"version": 2, "kind": "ci-deferred",
                                                     "snapshot": self.snapshot})
                self.assertEqual(self.pair.call_count, 2)
                self.publish.assert_not_called()

    def test_ci_deferral_cannot_mask_pair_or_proof_failure(self):
        self.producer.side_effect = bridge.shared.CIDeferred("pending")
        self.pair.side_effect = [None, bridge.shared.EventError("pair drift")]
        self.assertEqual(self.invoke_main(), (1, ""))
        self.pair.side_effect = None
        for error in (ValueError("pending"), bridge.shared.EventError("failed producer")):
            self.producer.side_effect = error
            self.assertEqual(self.invoke_main(), (1, ""))
        self.producer.side_effect = bridge.shared.CIDeferred("pending")
        self.analysis["detector_exit"] = 1
        self.assertEqual(self.invoke_main(), (1, ""))

    def test_api_rejects_all_write_payloads(self):
        api = bridge.ReadOnlyGitHub("read-only-test-token")
        with patch.object(bridge.shared.GitHub, "request", return_value={"ok": True}) as request:
            self.assertEqual(api.request("/repos/ben-ranford/lopper"), {"ok": True})
            with self.assertRaises(ValueError):
                api.request("/repos/ben-ranford/lopper/statuses/head", {})
            self.assertEqual(request.call_count, 1)


if __name__ == "__main__":
    unittest.main()

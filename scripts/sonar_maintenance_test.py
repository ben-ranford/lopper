"""Offline checks for project-bound permissions and owned settings transitions."""

import copy
import hashlib
import json
import unittest

import sonar_maintenance as maintenance


# These are synthetic reviewed inventory identities for offline controls only;
# they do not assert the repository's operational scanner parity.
SCOPE = json.dumps({"project": maintenance.PROJECT, "organization": maintenance.ORGANIZATION,
                    "properties": {"sonar.sources": "."}, "inventory": {
                        key: "a" * 64 for key in ("sources", "tests", "coverage", "qualityProfiles", "qualityGate")
                    }}, sort_keys=True).encode()
SCOPE_SHA = hashlib.sha256(SCOPE).hexdigest()


class Client:
    def __init__(self):
        self.calls = []
        self.saved = []
        self.denied = {}
        self.public = set()
        self.task = {"id": "task-1", "type": "REPORT", "componentKey": maintenance.PROJECT,
                     "componentId": "component-1", "status": "SUCCESS"}
        self.settings = {
            maintenance.REGEX_KEY: {"value": "(branch|release)-.*", "inherited": True},
            maintenance.AUTOSCAN_KEY: {"value": "true", "inherited": False},
        }
        self.original = copy.deepcopy(self.settings)
        self.active = []

    def save(self, state):
        self.saved.append(copy.deepcopy(state))

    def request(self, endpoint, parameters, *, authenticated=True, write=False):
        self.calls.append((endpoint, copy.deepcopy(parameters), authenticated, write))
        if not authenticated and endpoint not in self.public:
            raise maintenance.PermissionDenied(401)
        if endpoint in self.denied:
            raise maintenance.PermissionDenied(self.denied[endpoint])
        return self.dispatch(endpoint, parameters, write)

    def dispatch(self, endpoint, parameters, write):
        if endpoint == "components/show":
            return {"component": {"key": maintenance.PROJECT, "organization": maintenance.ORGANIZATION, "qualifier": "TRK"}}
        if endpoint == "ce/component":
            return {"current": copy.deepcopy(self.task)}
        if endpoint == "ce/task":
            return {"task": copy.deepcopy(self.task)}
        if endpoint == "ce/activity":
            tasks = self.active if "status" in parameters else [self.task]
            return {"tasks": copy.deepcopy(tasks), "paging": {"total": len(tasks), "pageIndex": 1, "pageSize": 1000}}
        if endpoint == "settings/values":
            return {"settings": [{"key": key, **value} for key, value in self.settings.items()]}
        if write:
            key = parameters["key"]
            self.settings[key] = copy.deepcopy(self.original[key]) if endpoint == "settings/reset" else {
                "value": parameters["value"], "inherited": False}
            return {}
        raise AssertionError("Unexpected endpoint")


class PermissionTests(unittest.TestCase):
    def test_task_and_admin_reads_are_distinct_and_do_not_claim_principal_or_submission(self):
        client = Client()
        proof = maintenance.probe_permissions(client)
        self.assertEqual(proof["task"], "task-1")
        self.assertTrue(proof["analysisRead"])
        self.assertTrue(proof["administrationRead"])
        self.assertEqual(proof["principal"], "UNKNOWN")
        self.assertEqual(proof["tokenClass"], "UNKNOWN")
        self.assertEqual(proof["settingsCompatibility"], "UNPROVEN")
        self.assertFalse(any(call[3] for call in client.calls))

    def test_denied_or_public_permission_endpoint_prevents_all_settings_writes(self):
        for endpoint in ("ce/task", "ce/activity"):
            for failure in ("denied", "public"):
                with self.subTest(endpoint=endpoint, failure=failure):
                    client = Client()
                    if failure == "denied":
                        client.denied[endpoint] = 403
                    else:
                        client.public.add(endpoint)
                    with self.assertRaises(maintenance.MaintenanceError):
                        maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
                    self.assertFalse(any(call[3] for call in client.calls))

    def test_foreign_or_missing_report_task_prevents_all_writes(self):
        for field, value in (("id", ""), ("type", "OTHER"), ("componentKey", "foreign"),
                             ("organization", "foreign"), ("componentId", "")):
            with self.subTest(field=field):
                client = Client()
                client.task[field] = value
                with self.assertRaises(maintenance.MaintenanceError):
                    maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
                self.assertFalse(any(call[3] for call in client.calls))

    def test_scope_unknown_blocks_before_permission_reads(self):
        client = Client()
        with self.assertRaisesRegex(maintenance.MaintenanceError, "scope"):
            maintenance.configure(client, "codex/v1.8.10-parent", None, None, save=client.save)
        self.assertEqual(client.calls, [])


class TransitionTests(unittest.TestCase):
    def test_regex_write_precedes_disable_and_inherited_restore_uses_reset(self):
        client = Client()
        state = maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
        writes = [call for call in client.calls if call[3]]
        self.assertEqual([call[1]["key"] for call in writes], [maintenance.REGEX_KEY, maintenance.AUTOSCAN_KEY])
        self.assertIn("(branch|release)-.*", state["expected"][maintenance.REGEX_KEY]["value"])
        self.assertEqual(client.settings[maintenance.AUTOSCAN_KEY]["value"], "false")
        maintenance.restore(client, state, save=client.save)
        self.assertEqual(client.settings, client.original)
        self.assertEqual([call[0] for call in client.calls if call[3]],
                         ["settings/set", "settings/set", "settings/reset", "settings/set"])

    def test_denied_first_owned_write_never_disables_autoscan(self):
        client = Client()
        client.denied["settings/set"] = 403
        with self.assertRaises(maintenance.PermissionDenied):
            maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
        self.assertEqual(client.settings, client.original)
        writes = [call for call in client.calls if call[3]]
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0][1]["key"], maintenance.REGEX_KEY)

    def test_external_change_and_unresolved_submission_prevent_restore(self):
        for failure in ("settings", "submission"):
            with self.subTest(failure=failure):
                client = Client()
                state = maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
                if failure == "settings":
                    client.settings[maintenance.REGEX_KEY]["value"] = "external"
                else:
                    state["submissionUnresolved"] = True
                before = len([call for call in client.calls if call[3]])
                with self.assertRaises(maintenance.MaintenanceError):
                    maintenance.restore(client, state, save=client.save)
                self.assertEqual(len([call for call in client.calls if call[3]]), before)

    def test_drain_queries_pending_and_running_and_rejects_late_success(self):
        client = Client()
        clock = iter((0, 0, 2))
        with self.assertRaisesRegex(maintenance.MaintenanceError, "deadline"):
            maintenance.drain(client, timeout=1, clock=lambda: next(clock), wait=lambda _: None)
        self.assertEqual(client.calls[0][1]["status"], "PENDING,IN_PROGRESS")
        client.active = [dict(client.task, status="UNKNOWN")]
        with self.assertRaisesRegex(maintenance.MaintenanceError, "Unknown"):
            maintenance.drain(client)

    def test_truncated_active_inventory_never_means_idle(self):
        with self.assertRaisesRegex(maintenance.MaintenanceError, "Incomplete"):
            maintenance.activity_tasks({"tasks": [], "paging": {"total": 1, "pageIndex": 1, "pageSize": 1000}})


if __name__ == "__main__":
    unittest.main()

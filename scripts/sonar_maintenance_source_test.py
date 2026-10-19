"""Real disposable source and archive controls; no official scanner is executed."""

import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import sonar_maintenance as maintenance
from sonar_maintenance_test import Client, SCOPE, SCOPE_SHA


class SourceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="lopper-sonar-source-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name).resolve()
        self.root = self.directory / "driver"
        self.root.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "maintenance.auto", "false")
        self.git("config", "gc.auto", "0")
        self.git("remote", "add", "origin", "https://github.com/ben-ranford/lopper.git")
        (self.root / "source.txt").write_text("reviewed source\n")
        self.git("add", "source.txt")
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        self.revision = self.git("rev-parse", "HEAD").strip()
        self.context = {"event": "workflow_dispatch", "repository": "ben-ranford/lopper",
                        "repositoryId": 1155023607, "ref": "refs/heads/preparation",
                        "workflowSHA": "a" * 40, "runId": 1, "runAttempt": 1}

    def git(self, *arguments):
        return subprocess.check_output(["/usr/bin/git", *arguments], cwd=self.root,
                                       env={"PATH": "/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM": "1",
                                            "GIT_CONFIG_GLOBAL": os.devnull}, text=True, stderr=subprocess.STDOUT)

    def bootstrap(self):
        return maintenance.verify_bootstrap(self.context, "a" * 40, "refs/heads/preparation",
                                            self.root, self.revision)

    def test_actual_source_and_platform_context_are_required_before_secret(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(self.bootstrap()["driver"], self.revision)
            for key, value in (("event", "pull_request"), ("repositoryId", 7),
                               ("ref", "refs/heads/other"), ("workflowSHA", "b" * 40)):
                with self.subTest(key=key), patch.dict(self.context, {key: value}):
                    with self.assertRaises(maintenance.MaintenanceError):
                        self.bootstrap()
        with patch.dict(os.environ, {"SONAR_TOKEN": "synthetic-sentinel"}, clear=True):
            with self.assertRaisesRegex(maintenance.MaintenanceError, "precede secret"):
                self.bootstrap()

    def test_modified_import_and_hidden_index_cannot_pass_clean_source(self):
        (self.root / "source.txt").write_text("changed source\n")
        with self.assertRaisesRegex(maintenance.MaintenanceError, "not clean"):
            maintenance.verify_checkout(self.root, self.revision)
        self.git("update-index", "--assume-unchanged", "source.txt")
        with self.assertRaisesRegex(maintenance.MaintenanceError, "index hides"):
            maintenance.verify_checkout(self.root, self.revision)
        self.git("update-index", "--no-assume-unchanged", "source.txt")
        self.git("checkout", "--", "source.txt")
        (self.root / "sitecustomize.py").write_text("raise RuntimeError('must not execute')\n")
        with self.assertRaisesRegex(maintenance.MaintenanceError, "not clean"):
            maintenance.verify_checkout(self.root, self.revision)

    def test_scanner_archive_checks_digest_and_rejects_traversal(self):
        archive = self.directory / "scanner.zip"
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("../escape", "bad")
        manifest = {"version": "fixture", "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}
        with self.assertRaisesRegex(maintenance.MaintenanceError, "Unsafe"):
            maintenance.scanner_archive(archive, self.directory / "extract", manifest)
        self.assertFalse((self.directory / "escape").exists())
        manifest["sha256"] = "0" * 64
        with self.assertRaisesRegex(maintenance.MaintenanceError, "digest"):
            maintenance.scanner_archive(archive, self.directory / "other", manifest)

    def test_scanner_uses_fixed_external_configuration_and_no_candidate_path(self):
        source = self.directory / "candidate"
        source.mkdir()
        (source / "sonar-project.properties").write_text("sonar.host.url=https://hostile.invalid\n")
        workspace = self.directory / "operation"
        workspace.mkdir()
        scanner = self.directory / "trusted-scanner"
        program = """#!/usr/bin/python3
import json, os, pathlib, sys
configuration = pathlib.Path(sys.argv[1].split('=', 1)[1])
properties = dict(line.split('=', 1) for line in configuration.read_text().splitlines())
(configuration.parent / 'observed.json').write_text(json.dumps({'arguments': sys.argv[1:], 'path': os.environ['PATH'], 'properties': properties}))
pathlib.Path(properties['sonar.scanner.metadataFilePath']).write_text('projectKey=ben-ranford_lopper\\nserverUrl=https://sonarcloud.io\\nceTaskId=fixture-task\\n')
print(os.environ['SONAR_TOKEN'])
"""
        scanner.write_text(program)
        scanner.chmod(0o700)
        properties = maintenance.scan_properties(maintenance.verified_scope(SCOPE, SCOPE_SHA),
                                                 reference="codex/v1.8.10-parent", revision=self.revision)
        self.assertEqual(maintenance.run_scanner(scanner, source, properties, workspace, "synthetic-token"), "fixture-task")
        observed = json.loads((workspace / "observed.json").read_text())
        self.assertEqual(observed["path"], "/usr/bin:/bin")
        self.assertEqual(observed["properties"]["sonar.host.url"], maintenance.ORIGIN)
        self.assertNotIn("synthetic-token", json.dumps(observed))
        self.assertEqual(observed["arguments"], ["-Dproject.settings=" + str(workspace / "scanner.properties")])

    def test_lost_submission_keeps_saved_uncertainty_and_never_restores(self):
        client = Client()
        client.token = "synthetic-sentinel"
        state = maintenance.configure(client, "codex/v1.8.10-parent", SCOPE, SCOPE_SHA, save=client.save)
        pair = {"base": self.revision, "head": self.revision, "base_ref": "codex/v1.8.10-parent", "pull_number": 1}
        self.git("update-ref", "refs/remotes/origin/" + pair["base_ref"], self.revision)
        def unavailable(*arguments):
            raise maintenance.MaintenanceError("lost task ID")
        with patch.object(maintenance, "scanner_archive", return_value=self.directory / "trusted-scanner"):
            with self.assertRaisesRegex(maintenance.MaintenanceError, "lost task ID"):
                maintenance.submit_analysis(client, pair, state, self.root, self.directory / "archive",
                                            self.directory, (SCOPE, SCOPE_SHA), is_child=False,
                                            head_ref=None, save=client.save, live_pair=lambda: copy.deepcopy(pair),
                                            manifest={}, scanner=unavailable)
        self.assertTrue(client.saved[-1]["submissionUnresolved"])
        writes = len([call for call in client.calls if call[3]])
        with self.assertRaisesRegex(maintenance.MaintenanceError, "may still be running"):
            maintenance.restore(client, state, save=client.save)
        self.assertEqual(len([call for call in client.calls if call[3]]), writes)


class ScopeTests(unittest.TestCase):
    def test_changed_scope_runtime_options_and_missing_inventory_are_rejected(self):
        with self.assertRaisesRegex(maintenance.MaintenanceError, "bytes changed"):
            maintenance.verified_scope(SCOPE + b" ", SCOPE_SHA)
        for field in ("sonar.host.url", "sonar.scanner.javaExePath", "sonar.scm.revision"):
            scope = json.loads(SCOPE)
            scope["properties"][field] = "candidate-controlled"
            data = json.dumps(scope).encode()
            digest = hashlib.sha256(data).hexdigest()
            with self.subTest(field=field), self.assertRaises(maintenance.MaintenanceError):
                maintenance.verified_scope(data, digest)
        scope = json.loads(SCOPE)
        del scope["inventory"]["coverage"]
        data = json.dumps(scope).encode()
        digest = hashlib.sha256(data).hexdigest()
        with self.assertRaisesRegex(maintenance.MaintenanceError, "Complete reviewed"):
            maintenance.verified_scope(data, digest)

    def test_modes_use_truthful_branch_or_pr_without_revision_override(self):
        scope = maintenance.verified_scope(SCOPE, SCOPE_SHA)
        base = maintenance.scan_properties(scope, reference="codex/v1.8.10-parent", revision="a" * 40)
        child = maintenance.scan_properties(scope, reference="codex/v1.8.10-parent", revision="b" * 40,
                                            pull_number=42, head_ref="codex/v1.8.10-child")
        self.assertEqual(base["sonar.branch.name"], "codex/v1.8.10-parent")
        self.assertFalse(any(key.startswith("sonar.pullrequest.") for key in base))
        self.assertEqual(child["sonar.pullrequest.base"], "codex/v1.8.10-parent")
        self.assertFalse(any(key.startswith("sonar.branch.") for key in child))
        self.assertNotIn("sonar.scm.revision", base)
        self.assertNotIn("sonar.scm.revision", child)


class ParentAndCleanupTests(unittest.TestCase):
    def test_parent_history_rejects_invalid_ambiguous_and_stale_identity(self):
        reference = "codex/v1.8.10-parent"
        branches = {"branches": [{"name": reference, "type": "LONG"}]}
        row = {"key": "one", "revision": "a" * 40, "date": "2026-10-01T00:00:00Z"}
        def verify(rows):
            return maintenance.parent_baseline(branches, {"analyses": rows,
                "paging": {"pageIndex": 1, "total": len(rows)}}, reference, "a" * 40)
        self.assertEqual(verify([row])["analysisId"], "one")
        for rows in ([dict(row, date="invalid")], [dict(row, date="2026-99-01T00:00:00Z")],
                     [row, row], [row, dict(row, key="two")], [dict(row, revision="b" * 40)]):
            with self.subTest(rows=rows), self.assertRaises(maintenance.MaintenanceError):
                verify(rows)

    def test_term_refusal_still_attempts_due_kill_and_retains_uncertainty(self):
        from unittest.mock import Mock, call
        import signal
        process = Mock(pid=123)
        process.wait.side_effect = [subprocess.TimeoutExpired("synthetic", 2), 0]
        with patch("os.killpg", side_effect=[PermissionError("synthetic"), None]) as send:
            with self.assertRaisesRegex(maintenance.MaintenanceError, "cleanup could not be verified"):
                maintenance.stop_scanner(process, signal.SIGTERM, signal.SIGKILL)
        self.assertEqual(send.call_args_list, [call(123, signal.SIGTERM), call(123, signal.SIGKILL)])
        self.assertEqual(process.wait.call_args_list, [call(timeout=2), call(timeout=10)])


if __name__ == "__main__":
    unittest.main()

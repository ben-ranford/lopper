"""Artifact-level regression tests for the Python runtime import hook."""

from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HOOK_DIR = Path(__file__).resolve().parent


class SitecustomizeArtifactPrivacyTest(unittest.TestCase):
    def test_persists_module_identifiers_without_host_paths(self) -> None:
        with tempfile.TemporaryDirectory(prefix="lopper-runtime-python-") as fixture:
            fixture_root = Path(fixture)
            repo_root = fixture_root / "repo"
            site_packages = fixture_root / "python" / "site-packages"
            package_root = site_packages / "thirdparty"
            trace_path = fixture_root / "python.ndjson"
            entrypoint = repo_root / "main.py"

            package_root.mkdir(parents=True)
            repo_root.mkdir()
            (package_root / "__init__.py").write_text("VALUE = 1\n", encoding="utf-8")
            entrypoint.write_text("import thirdparty\n", encoding="utf-8")

            env = os.environ.copy()
            env.pop("PYTHONHOME", None)
            env["LOPPER_RUNTIME_REPO_ROOT"] = str(repo_root)
            env["LOPPER_RUNTIME_TRACE"] = str(trace_path)
            env["PYTHONPATH"] = os.pathsep.join((str(HOOK_DIR), str(site_packages)))
            subprocess.run(
                [sys.executable, str(entrypoint)],
                cwd=repo_root,
                env=env,
                check=True,
                capture_output=True,
                text=True,
            )

            events = [
                json.loads(line)
                for line in trace_path.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]
            self.assertTrue(events)
            artifact = json.dumps(events, sort_keys=True)
            for forbidden in (
                str(fixture_root),
                str(repo_root),
                str(site_packages),
                str(HOOK_DIR),
                "site-packages",
                "file://",
            ):
                self.assertNotIn(forbidden, artifact)

            event = next(item for item in events if item.get("module") == "thirdparty")
            self.assertEqual(event["language"], "python")
            self.assertEqual(event["dependency"], "thirdparty")
            self.assertEqual(event["resolved"], "thirdparty")
            self.assertEqual(event["entrypoint"], "main.py")


if __name__ == "__main__":
    unittest.main()

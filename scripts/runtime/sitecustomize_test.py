"""Artifact-level regression tests for the Python runtime import hook."""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import uuid


HOOK_DIR = Path(__file__).resolve().parent


def load_sitecustomize_module(repo_root: Path):
    module_name = f"sitecustomize_test_{uuid.uuid4().hex}"
    spec = importlib.util.spec_from_file_location(module_name, HOOK_DIR / "sitecustomize.py")
    if spec is None or spec.loader is None:
        raise RuntimeError("load sitecustomize spec")

    old_trace = os.environ.get("LOPPER_RUNTIME_TRACE")
    old_repo_root = os.environ.get("LOPPER_RUNTIME_REPO_ROOT")
    os.environ.pop("LOPPER_RUNTIME_TRACE", None)
    os.environ["LOPPER_RUNTIME_REPO_ROOT"] = str(repo_root)

    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    try:
        spec.loader.exec_module(module)
    finally:
        if old_trace is None:
            os.environ.pop("LOPPER_RUNTIME_TRACE", None)
        else:
            os.environ["LOPPER_RUNTIME_TRACE"] = old_trace
        if old_repo_root is None:
            os.environ.pop("LOPPER_RUNTIME_REPO_ROOT", None)
        else:
            os.environ["LOPPER_RUNTIME_REPO_ROOT"] = old_repo_root
    return module_name, module


class SitecustomizeArtifactPrivacyTest(unittest.TestCase):
    def test_normalize_repo_context_rejects_hostile_paths_before_realpath(self) -> None:
        with tempfile.TemporaryDirectory(prefix="lopper-runtime-python-") as fixture:
            fixture_root = Path(fixture)
            repo_root = fixture_root / "repo"
            repo_root.mkdir()
            module_name, module = load_sitecustomize_module(repo_root)
            self.addCleanup(sys.modules.pop, module_name, None)

            calls: list[str] = []

            def fail_real_path(path: str) -> str:
                calls.append(path)
                raise AssertionError(f"_real_path should not be called for hostile input: {path}")

            module._real_path = fail_real_path
            for value in (
                str(fixture_root / "private.py"),
                str(repo_root / ".." / "private.py"),
                "../private.py",
            ):
                self.assertEqual(module._normalize_repo_context(value), "")
            self.assertEqual(calls, [])

    def test_normalize_repo_context_realpaths_trusted_repo_local_paths(self) -> None:
        with tempfile.TemporaryDirectory(prefix="lopper-runtime-python-") as fixture:
            fixture_root = Path(fixture)
            repo_root = fixture_root / "repo"
            entrypoint = repo_root / "main.py"
            repo_root.mkdir()
            entrypoint.write_text("print('ok')\n", encoding="utf-8")

            module_name, module = load_sitecustomize_module(repo_root)
            self.addCleanup(sys.modules.pop, module_name, None)

            original_real_path = module._real_path
            calls: list[str] = []

            def track_real_path(path: str) -> str:
                calls.append(path)
                return original_real_path(path)

            module._real_path = track_real_path
            cwd = os.getcwd()
            try:
                os.chdir(repo_root)
                self.assertEqual(module._normalize_repo_context("main.py"), "main.py")
            finally:
                os.chdir(cwd)
            self.assertEqual(module._normalize_repo_context(str(entrypoint)), "main.py")
            self.assertGreaterEqual(len(calls), 2)

    def test_symlinked_repo_root_trust_is_stable_and_escape_safe(self) -> None:
        with tempfile.TemporaryDirectory(prefix="lopper-runtime-python-") as fixture:
            fixture_root = Path(fixture)
            repo_root = fixture_root / "repo-real"
            alias_root = fixture_root / "repo-alias"
            outside_root = fixture_root / "outside"
            entrypoint = repo_root / "src" / "main.py"
            escaped_target = outside_root / "private.py"
            escaped_link = repo_root / "src" / "escaped.py"

            entrypoint.parent.mkdir(parents=True)
            outside_root.mkdir()
            entrypoint.write_text("print('ok')\n", encoding="utf-8")
            escaped_target.write_text("print('private')\n", encoding="utf-8")
            alias_root.symlink_to(repo_root, target_is_directory=True)
            escaped_link.symlink_to(escaped_target)

            module_name, module = load_sitecustomize_module(alias_root)
            self.addCleanup(sys.modules.pop, module_name, None)

            self.assertEqual(
                module._normalize_repo_context(os.path.realpath(entrypoint)),
                "src/main.py",
            )
            self.assertEqual(
                module._normalize_repo_context(str(alias_root / "src" / "main.py")),
                "src/main.py",
            )
            self.assertEqual(module._normalize_repo_context(str(escaped_link)), "")

            alias_root.unlink()
            alias_root.symlink_to(outside_root, target_is_directory=True)
            self.assertEqual(module._normalize_repo_context(str(alias_root / "private.py")), "")

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

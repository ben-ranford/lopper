"""Artifact-level regression tests for the Python runtime import hook."""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import errno
import subprocess
import sys
import tempfile
import unittest
import uuid


HOOK_DIR = Path(__file__).resolve().parent


def _is_skippable_symlink_error(error: OSError) -> bool:
    return error.errno in {errno.EPERM, errno.EACCES} or getattr(error, "winerror", None) == 1314


def _create_windows_directory_junction(link_path: Path, target_path: Path) -> None:
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(link_path), str(target_path)],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        stderr = (result.stderr or "").strip()
        stdout = (result.stdout or "").strip()
        detail = stderr or stdout or f"exit status {result.returncode}"
        raise OSError(f"failed to create junction {link_path} -> {target_path}: {detail}")


def _remove_directory_alias(link_path: Path) -> None:
    if os.name == "nt":
        result = subprocess.run(
            ["cmd.exe", "/d", "/c", "rmdir", str(link_path)],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            stderr = (result.stderr or "").strip()
            stdout = (result.stdout or "").strip()
            detail = stderr or stdout or f"exit status {result.returncode}"
            raise OSError(f"failed to remove directory alias {link_path}: {detail}")
        return
    link_path.unlink()


def _create_test_symlink(test_case: unittest.TestCase, link_path: Path, target_path: Path, *, is_dir: bool = False) -> None:
    if is_dir and os.name == "nt":
        _create_windows_directory_junction(link_path, target_path)
        return
    try:
        link_path.symlink_to(target_path, target_is_directory=is_dir)
    except OSError as error:
        if _is_skippable_symlink_error(error):
            test_case.skipTest("symlink creation requires elevated privileges or Developer Mode on this platform")
        raise


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
    def test_create_windows_directory_junction_uses_safe_argv(self) -> None:
        if os.name != "nt":
            self.skipTest("Windows-only junction coverage")

        captured: dict[str, object] = {}
        original_run = subprocess.run

        def fake_run(args, **kwargs):
            captured["args"] = args
            captured["kwargs"] = kwargs

            class Result:
                returncode = 0
                stdout = ""
                stderr = ""

            return Result()

        subprocess.run = fake_run
        try:
            _create_windows_directory_junction(Path(r"C:\repo-alias"), Path(r"C:\repo-real"))
        finally:
            subprocess.run = original_run

        self.assertEqual(
            captured["args"],
            ["cmd.exe", "/d", "/c", "mklink", "/J", r"C:\repo-alias", r"C:\repo-real"],
        )
        self.assertEqual(
            captured["kwargs"],
            {"capture_output": True, "text": True, "check": False},
        )

    def test_remove_directory_alias_uses_safe_argv(self) -> None:
        if os.name != "nt":
            self.skipTest("Windows-only junction coverage")

        captured: dict[str, object] = {}
        original_run = subprocess.run

        def fake_run(args, **kwargs):
            captured["args"] = args
            captured["kwargs"] = kwargs

            class Result:
                returncode = 0
                stdout = ""
                stderr = ""

            return Result()

        subprocess.run = fake_run
        try:
            _remove_directory_alias(Path(r"C:\repo-alias"))
        finally:
            subprocess.run = original_run

        self.assertEqual(
            captured["args"],
            ["cmd.exe", "/d", "/c", "rmdir", r"C:\repo-alias"],
        )
        self.assertEqual(
            captured["kwargs"],
            {"capture_output": True, "text": True, "check": False},
        )

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

    @unittest.skipIf(os.name == "nt", "Windows does not support control whitespace in filenames")
    def test_redacts_control_whitespace_from_context_helpers_and_import_events(self) -> None:
        with tempfile.TemporaryDirectory(prefix="lopper-runtime-python-controls-") as fixture:
            fixture_root = Path(fixture)
            repo_root = fixture_root / "repo"
            site_packages = fixture_root / "python" / "site-packages"
            package_root = site_packages / "thirdparty"
            package_root.mkdir(parents=True)
            repo_root.mkdir()
            (package_root / "__init__.py").write_text("VALUE = 1\n", encoding="utf-8")

            module_name, module = load_sitecustomize_module(repo_root)
            self.addCleanup(sys.modules.pop, module_name, None)

            safe_path = repo_root / "hello world-\u4e16\u754c.py"
            safe_path.write_text("print('ok')\n", encoding="utf-8")
            self.assertEqual(
                module._normalize_repo_context(str(safe_path)),
                "hello world-\u4e16\u754c.py",
            )

            cases = (
                ("newline", "\n"),
                ("carriage return", "\r"),
                ("tab", "\t"),
            )
            for name, character in cases:
                with self.subTest(name=name):
                    entrypoint = repo_root / f"main{character}context.py"
                    trace_path = fixture_root / f"{name.replace(' ', '-')}.ndjson"
                    entrypoint.write_text("import thirdparty\n", encoding="utf-8")
                    self.assertEqual(module._normalize_repo_context(str(entrypoint)), "")

                    env = os.environ.copy()
                    env.pop("PYTHONHOME", None)
                    env["LOPPER_RUNTIME_REPO_ROOT"] = str(repo_root)
                    env["LOPPER_RUNTIME_TRACE"] = str(trace_path)
                    env["PYTHONDONTWRITEBYTECODE"] = "1"
                    env["PYTHONPATH"] = os.pathsep.join((str(HOOK_DIR), str(site_packages)))
                    subprocess.run(
                        [sys.executable, str(entrypoint)],
                        cwd=repo_root,
                        env=env,
                        check=True,
                        capture_output=True,
                        text=True,
                    )

                    artifact = trace_path.read_text(encoding="utf-8")
                    escaped_filename = json.dumps(entrypoint.name)[1:-1]
                    self.assertNotIn(escaped_filename, artifact)
                    events = [
                        json.loads(line)
                        for line in artifact.splitlines()
                        if line.strip()
                    ]
                    event = next(item for item in events if item.get("module") == "thirdparty")
                    self.assertEqual(event["parent"], "")
                    self.assertEqual(event["entrypoint"], "")
                    for field in ("parent", "entrypoint"):
                        self.assertFalse(any(control in event[field] for control in "\n\r\t"))

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
            _create_test_symlink(self, alias_root, repo_root, is_dir=True)
            _create_test_symlink(self, escaped_link, escaped_target)

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

            _remove_directory_alias(alias_root)
            _create_test_symlink(self, alias_root, outside_root, is_dir=True)
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

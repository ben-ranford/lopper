#!/usr/bin/env python3
"""Offline runner contracts: no Go tool installation, execution or network."""
import argparse
from contextlib import redirect_stderr, redirect_stdout
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

import check_gostyle_regression as gate

HERE = Path(__file__).resolve().parent


def completed(command, code=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(command, code, stdout, stderr)


class FakeCommands:
    def __init__(self, root):
        self.root = root
        self.calls = []
        self.fixture_code = 3
        self.fixture_extra = ""
        self.fixture_stdout = ""
        self.repository_code = 0
        self.fixture_timeout = False
        self.setup_failure = None
        self.graph = {"Path": gate.MODULE, "Version": "v0.26.2"}

    def execute(self, command, cwd, environment, timeout=None):
        self.calls.append((command, Path(cwd), dict(environment), timeout))
        if command[1] == "env":
            return completed(command, stdout=json.dumps({"GOROOT": str(self.root / "compiler"),
                "GOEXE": "", "GOMODCACHE": "/existing/modules", "GOCACHE": "/existing/build"}))
        if command[1] == "run":
            return self.scan(command, Path(cwd))
        if command[1:] == self.setup_failure:
            return completed(command, 1, stderr="module integrity failed")
        if command[1:4] == ["list", "-m", "-json"]:
            return completed(command, stdout=json.dumps(self.graph))
        return completed(command, stdout="all modules verified\n")

    def scan(self, command, cwd):
        if cwd == self.root:
            return completed(command, self.repository_code, stderr="repository diagnostic\n" if self.repository_code else "")
        if self.fixture_timeout:
            raise subprocess.TimeoutExpired(command, gate.FIXTURE_TIMEOUT)
        expected = json.loads((cwd / "expected.json").read_text())
        lines = "".join(f"{cwd.resolve() / item['path']}:{item['line']}:{item['column']}: {item['message']}\n" for item in expected)
        return completed(command, self.fixture_code, self.fixture_stdout, lines + self.fixture_extra)


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="gostyle-offline-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        fixtures = self.root / "scripts/testdata/gostyle-regression"
        shutil.copytree(HERE / "testdata/gostyle-regression", fixtures)
        self.config = self.root / ".gostyle.yml"
        self.config.write_text("analyzers: {}\n")
        self.args = argparse.Namespace(go="go", toolchain="go1.27.2", version="v0.26.2", root=str(self.root), config=str(self.config))
        self.fake = FakeCommands(self.root)
        self.output = io.StringIO()

    def invoke(self):
        with mock.patch.object(gate, "execute", self.fake.execute), redirect_stdout(self.output), redirect_stderr(self.output):
            return gate.run(self.args)

    def scans(self):
        return [call for call in self.fake.calls if call[0][1] == "run"]

    def test_arbitrary_launcher_rejected_before_any_command(self):
        for launcher in ["python3", "go --help", "-version", str(self.root / "untrusted/go")]:
            with self.subTest(launcher=launcher):
                self.args.go = launcher
                self.fake.calls.clear()
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(self.fake.calls)
                self.assertIn("configured Go launcher", self.output.getvalue())

    def test_one_verified_build_two_scans_and_owned_cleanup(self):
        self.assertEqual(self.invoke(), 0)
        installs = [call for call in self.fake.calls if call[0][1] == "install"]
        self.assertEqual(len(installs), 1)
        scans = self.scans()
        self.assertEqual(len(scans), 2)
        self.assertEqual(scans[0][0], scans[1][0])
        self.assertEqual(scans[0][0][2:], ["-c", str(self.config), "./..."])
        self.assertEqual(scans[0][3], 60)
        self.assertIsNone(scans[1][3])
        self.assertEqual(scans[1][1], self.root)
        self.assertFalse(Path(installs[0][2]["GOBIN"]).parent.exists())
        arguments = [call[0][1:] for call in self.fake.calls]
        self.assertLess(arguments.index(["mod", "verify"]), arguments.index(["install", gate.MODULE + "@v0.26.2"]))
        self.assertEqual(self.config.read_text(), "analyzers: {}\n")

    def test_fixture_data_materialises_exact_owned_path(self):
        source = self.root / "scripts/testdata/gostyle-regression/anonymous/fixture_test.go.txt"
        original = source.read_bytes()
        observed = []
        scan = self.fake.scan

        def inspect(command, cwd):
            if cwd != self.root:
                destination = cwd / "anonymous/fixture_test.go"
                self.assertEqual(destination.read_bytes(), original)
                self.assertFalse((cwd / "anonymous/fixture_test.go.txt").exists())
                self.assertFalse(cwd.is_relative_to(self.root))
                observed.append(destination)
            return scan(command, cwd)

        with mock.patch.object(self.fake, "scan", inspect):
            self.assertEqual(self.invoke(), 0)
        self.assertEqual(len(observed), 1)
        self.assertFalse(observed[0].exists())
        self.assertEqual(source.read_bytes(), original)
        self.assertFalse(source.with_suffix("").exists())

    def test_missing_fixture_data_still_scans_and_cleans(self):
        source = self.root / "scripts/testdata/gostyle-regression/anonymous/fixture_test.go.txt"
        source.unlink()
        self.assertEqual(self.invoke(), 1)
        self.assertEqual(len(self.scans()), 1)
        self.assertEqual(self.scans()[0][1], self.root)
        self.assertFalse(Path(self.scans()[0][0][0]).parent.parent.exists())

    def test_version_grammar_rejects_unicode_and_option_injection(self):
        for version in ["v٠.26.2", "v0.２6.2", "v0.26.²", "v0.26.2;echo bad", "-modfile=bad"]:
            with self.subTest(version=version):
                self.args.version = version
                self.fake.calls.clear()
                self.assertEqual(self.invoke(), 1)
                self.assertEqual(len(self.fake.calls), 1)
                self.assertIn("explicit release or pseudo-version", self.output.getvalue())
        self.args.version = "v0.26.2-0.20261009074109-a852d2403d81"
        self.fake.graph["Version"] = self.args.version
        self.assertEqual(self.invoke(), 0)

    def test_cache_authentication_and_loader_compiler(self):
        with mock.patch.dict(os.environ, {"GOPROXY": "off", "GOSUMDB": "off", "GONOSUMDB": "*", "GOFLAGS": "-overlay=bad", "GOCACHEPROG": "bad"}):
            self.assertEqual(self.invoke(), 0)
        environment = self.scans()[0][2]
        self.assertEqual(environment["GOMODCACHE"], "/existing/modules")
        self.assertEqual(environment["GOCACHE"], "/existing/build")
        self.assertEqual(environment["GOPROXY"], "https://proxy.golang.org")
        self.assertEqual(environment["GOSUMDB"], "sum.golang.org")
        self.assertEqual(environment["GONOSUMDB"], "")
        self.assertEqual(environment["GOFLAGS"], "-buildvcs=false")
        self.assertEqual(environment["GOTOOLCHAIN"], "local")
        self.assertEqual(environment["GOENV"], "off")
        self.assertNotIn("GOCACHEPROG", environment)
        self.assertEqual(environment["PATH"].split(os.pathsep)[0], str(self.root / "compiler/bin"))

    def test_inherited_goroot_is_removed_before_compiler_probe(self):
        for inherited in [str(self.root), str(self.root / "incomplete-sdk")]:
            with self.subTest(goroot=inherited), mock.patch.dict(os.environ, {"GOROOT": inherited}):
                before = dict(os.environ)
                self.fake.calls.clear()
                self.assertEqual(self.invoke(), 0)
                probe = self.fake.calls[0]
                self.assertEqual(probe[0][1:3], ["env", "-json"])
                self.assertEqual(probe[2]["GOTOOLCHAIN"], "go1.27.2")
                self.assertNotIn("GOROOT", probe[2])
                compiler = str(self.root / "compiler/bin/go")
                for command, _, environment, _ in self.fake.calls[1:]:
                    self.assertNotIn("GOROOT", environment)
                    self.assertEqual(environment["PATH"].split(os.pathsep)[0], str(Path(compiler).parent))
                    if command[1] != "run":
                        self.assertEqual(command[0], compiler)
                self.assertEqual(os.environ, before)

    def test_direct_panic_and_silent_success_still_scan_repository(self):
        for code in [0, 1, 2, 4]:
            with self.subTest(code=code):
                self.fake.calls.clear()
                self.fake.fixture_code = code
                self.assertEqual(self.invoke(), 1)
                self.assertEqual(len(self.scans()), 2)
                self.assertIn(f"got {code}", self.output.getvalue())

    def test_unexpected_diagnostics_and_stdout_still_scan(self):
        for extra, stdout in [("panic: index out of range\n", ""), ("import failure\n", ""), ("valid/fixture.go:1:1: finding\n", ""), ("", "unexpected")]:
            with self.subTest(extra=extra, stdout=stdout):
                self.fake.calls.clear()
                self.fake.fixture_extra, self.fake.fixture_stdout = extra, stdout
                self.assertEqual(self.invoke(), 1)
                self.assertEqual(len(self.scans()), 2)
                self.assertIn(extra or stdout, self.output.getvalue())

    def test_timeout_and_corrupt_expectations_still_scan(self):
        self.fake.fixture_timeout = True
        self.assertEqual(self.invoke(), 1)
        self.assertEqual(len(self.scans()), 2)
        self.fake.fixture_timeout = False
        self.fake.calls.clear()
        (self.root / "scripts/testdata/gostyle-regression/expected.json").write_text('{"wrong":"shape"}')
        self.assertEqual(self.invoke(), 1)
        self.assertEqual(self.scans()[-1][1], self.root)

    def test_repository_failure_cannot_be_hidden_by_fixture(self):
        self.fake.repository_code = 3
        self.assertEqual(self.invoke(), 1)
        self.assertIn("repository diagnostic", self.output.getvalue())
        self.assertIn("repository scan: exit 3", self.output.getvalue())

    def test_modified_module_rejected_before_build(self):
        self.fake.setup_failure = ["mod", "verify"]
        self.assertEqual(self.invoke(), 1)
        self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))
        self.assertIn("module integrity failed", self.output.getvalue())

    def test_setup_timeout_and_fixture_launch_error_cleanup(self):
        real_fake = self.fake.execute
        def timed_setup(command, cwd, environment, timeout=None):
            if command[1] == "get":
                raise subprocess.TimeoutExpired(command, timeout)
            return real_fake(command, cwd, environment, timeout)
        with mock.patch.object(gate, "execute", timed_setup), redirect_stderr(self.output):
            self.assertEqual(gate.run(self.args), 1)
        self.assertFalse(self.scans())
        def failed_fixture(command, cwd, environment, timeout=None):
            if command[1] == "run" and Path(cwd) != self.root:
                self.fake.calls.append((command, Path(cwd), environment, timeout))
                raise OSError("fixture launch failed")
            return real_fake(command, cwd, environment, timeout)
        with mock.patch.object(gate, "execute", failed_fixture), redirect_stdout(self.output), redirect_stderr(self.output):
            self.assertEqual(gate.run(self.args), 1)
        self.assertEqual(self.scans()[-1][1], self.root)
        binary = Path(self.scans()[-1][0][0])
        self.assertFalse(binary.parent.parent.exists())

    def test_replaced_wrong_or_missing_module_rejected(self):
        for graph in [{"Path": gate.MODULE, "Version": "v0.26.2", "Replace": {}}, {"Path": gate.MODULE, "Version": "v0.0.1"}, {"Path": "other"}]:
            with self.subTest(graph=graph):
                self.fake.graph = graph
                self.fake.calls.clear()
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))


class LauncherTests(unittest.TestCase):
    def test_fixed_lookup_and_canonical_absolute_override(self):
        with tempfile.TemporaryDirectory(prefix="gostyle-launcher-") as directory:
            launcher = Path(directory).resolve() / ("go.exe" if os.name == "nt" else "go")
            launcher.write_text("fixture executable")
            launcher.chmod(0o755)
            with mock.patch.object(os, "get_exec_path", return_value=[str(launcher.parent)]):
                self.assertEqual(gate.admitted_launcher("go"), str(launcher))
                self.assertEqual(gate.admitted_launcher(str(launcher)), str(launcher))

    def test_missing_or_nonregular_launcher_fails_closed(self):
        with mock.patch.object(os, "get_exec_path", return_value=[]):
            with self.assertRaisesRegex(gate.GateError, "unavailable"):
                gate.admitted_launcher("go")
        with tempfile.TemporaryDirectory(prefix="gostyle-launcher-") as directory:
            (Path(directory) / ("go.exe" if os.name == "nt" else "go")).mkdir()
            with mock.patch.object(os, "get_exec_path", return_value=[directory, ".", ""]):
                with self.assertRaisesRegex(gate.GateError, "unavailable"):
                    gate.admitted_launcher("go")


class DiagnosticTests(unittest.TestCase):
    def test_missing_duplicate_position_and_control_diagnostics(self):
        expected = json.loads((HERE / "testdata/gostyle-regression/expected.json").read_text())
        fixture = Path("/fixture")
        item = expected[0]
        line = f"{fixture / item['path']}:{item['line']}:{item['column']}: {item['message']}"
        gate.verify_diagnostics(completed([], 3, stderr=line + "\n"), expected, fixture)
        for invalid in ["", line + "\n" + line, line.replace(":6:2:", ":6:3:"), line.replace("anonymous/fixture_test.go", "valid/fixture.go")]:
            result = completed([], 3, stderr=invalid)
            with self.subTest(invalid=invalid), self.assertRaises(gate.GateError):
                gate.verify_diagnostics(result, expected, fixture)


if __name__ == "__main__":
    unittest.main()

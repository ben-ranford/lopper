#!/usr/bin/env python3
"""Offline runner contracts: no Go tool installation, execution or network."""
import argparse
import copy
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
        self.reference = json.loads((root / "scripts/testdata/gostyle-regression/upstream-receipt.json").read_text())
        self.graph = {key: self.reference[key] for key in ("Path", "Version", "Sum", "GoModSum")}
        self.graph_versions = dict(self.reference["graph_versions"])
        self.build_extra = []
        self.build_missing = False
        self.download = copy.deepcopy(self.reference)
        self.importer = copy.deepcopy(self.reference["tools"])
        self.metadata = "\n".join("\t".join([kind, item["Path"], item["Version"], item["Sum"]])
                                  for kind, item in [("mod", self.reference), ("dep", self.importer)])

    def execute(self, command, cwd, environment, timeout=None):
        self.calls.append((command, Path(cwd), dict(environment), timeout))
        if command[1] == "env":
            return completed(command, stdout=json.dumps({"GOROOT": str(self.root / "compiler"),
                "GOEXE": "", "GOMODCACHE": "/existing/modules", "GOCACHE": "/existing/build"}))
        if command[1] == "run":
            return self.scan(command, Path(cwd))
        if command[1:] == self.setup_failure:
            return completed(command, 1, stderr="module integrity failed")
        if command[1:4] == ["mod", "download", "-json"]:
            data = self.download if command[4].startswith(gate.MODULE + "@") else self.importer
            return completed(command, stdout=json.dumps(data))
        if command[1:4] == ["list", "-m", "-json"]:
            records = [self.graph, self.importer]
            records.extend({"Path": path, "Version": version} for path, version in self.graph_versions.items()
                           if path not in (gate.MODULE, self.reference["tools"]["Path"]))
            return completed(command, stdout="\n".join(json.dumps(record) for record in records))
        if command[1:4] == ["list", "-deps", "-json"]:
            modules = [] if self.build_missing else [self.graph, self.importer]
            return completed(command, stdout="\n".join(json.dumps({"Module": record}) for record in modules + self.build_extra))
        if command[1:3] == ["version", "-m"]:
            return completed(command, stdout=self.metadata)
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


    def test_download_identity_origin_and_checksum_fail_before_install(self):
        changes = {"Path": "other", "Version": "v0.0.1", "Sum": "h1:wrong", "GoModSum": "h1:wrong", "Origin": {"Hash": "wrong"}}
        for key, value in changes.items():
            with self.subTest(key=key):
                self.fake.calls.clear()
                self.fake.download = copy.deepcopy(self.fake.reference)
                self.fake.download[key] = value
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))
                self.assertFalse(self.scans())

    def test_configured_version_requires_deliberate_receipt_update(self):
        self.args.version = "v0.26.3"
        self.assertEqual(self.invoke(), 1)
        self.assertFalse(self.scans())
        self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))

    def test_closure_missing_tampered_importer_and_checksum_rejected(self):
        for key, value in [("Sum", "h1:changed"), ("GoModSum", "h1:changed"), ("Version", "v0.45.0"), ("Path", "unexpected/module")]:
            with self.subTest(key=key):
                self.fake.calls.clear()
                self.fake.importer = copy.deepcopy(self.fake.reference["tools"])
                self.fake.importer[key] = value
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))
                self.assertFalse(self.scans())

    def test_binary_provenance_failure_prevents_tool_use(self):
        original = self.fake.metadata
        for metadata in [original.replace("v0.26.2", "v0.0.1"), original.replace("v0.50.0", "v0.45.0"), original.replace("h1:", "h1:wrong"), original + "\n=>\tprivate/fork", original + "\nmod\tprivate/fork\t(devel)", original.splitlines()[0]]:
            with self.subTest(metadata=metadata):
                self.fake.calls.clear()
                self.fake.metadata = metadata
                self.assertEqual(self.invoke(), 1)
                self.assertEqual(sum(call[0][1] == "install" for call in self.fake.calls), 1)
                self.assertFalse(self.scans())



    def test_graph_only_versions_are_bound_without_invented_source_sums(self):
        self.assertEqual(self.invoke(), 0)
        for change in ("missing", "extra", "version"):
            with self.subTest(change=change):
                self.fake.calls.clear()
                self.fake.graph_versions = dict(self.fake.reference["graph_versions"])
                if change == "missing":
                    del self.fake.graph_versions["github.com/cpuguy83/go-md2man/v2"]
                elif change == "extra":
                    self.fake.graph_versions["unexpected/module"] = "v1.0.0"
                else:
                    self.fake.graph_versions["github.com/cpuguy83/go-md2man/v2"] = "v0.0.1"
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))

    def test_build_closure_missing_extra_replaced_and_platform_module(self):
        self.fake.build_missing = True
        self.assertEqual(self.invoke(), 1)
        self.fake.build_missing = False
        for module in [{"Path": "unexpected/module", "Version": "v1.0.0"}, self.fake.graph | {"Replace": {}}]:
            with self.subTest(module=module):
                self.fake.calls.clear()
                self.fake.build_extra = [module]
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(any(call[0][1] == "install" for call in self.fake.calls))
        checksums = gate.checksum_records((self.root / "scripts/testdata/gostyle-regression/upstream-go.sum").read_text())
        path, version = "golang.org/x/sys", self.fake.reference["graph_versions"]["golang.org/x/sys"]
        self.fake.build_extra = [{"Path": path, "Version": version, "Sum": checksums[(path, version)], "GoModSum": checksums[(path, version + "/go.mod")]}]
        self.assertEqual(self.invoke(), 0)



    def test_arbitrary_launcher_rejected_before_any_command(self):
        for launcher in ["python3", "go --help", "-version", str(self.root / "untrusted/go")]:
            with self.subTest(launcher=launcher):
                self.args.go = launcher
                self.fake.calls.clear()
                self.assertEqual(self.invoke(), 1)
                self.assertFalse(self.fake.calls)
                self.assertIn("configured Go launcher", self.output.getvalue())

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
                grouped = self.root / "scripts/testdata/gostyle-regression/multiple/fixture.go.txt"
                self.assertEqual((cwd / "multiple/fixture.go").read_bytes(), grouped.read_bytes())
                self.assertFalse((cwd / "multiple/fixture.go.txt").exists())
                observed.append(destination)
            return scan(command, cwd)

        with mock.patch.object(self.fake, "scan", inspect):
            self.assertEqual(self.invoke(), 0)
        self.assertEqual(len(observed), 1)
        self.assertFalse(observed[0].exists())
        self.assertEqual(source.read_bytes(), original)
        self.assertFalse(source.with_suffix("").exists())
        grouped = self.root / "scripts/testdata/gostyle-regression/multiple/fixture.go.txt"
        self.assertEqual(grouped.read_bytes(), (HERE / "testdata/gostyle-regression/multiple/fixture.go.txt").read_bytes())
        self.assertFalse(grouped.with_suffix("").exists())

    def test_named_fixture_data_materialises_exact_owned_path(self):
        source = self.root / "scripts/testdata/gostyle-regression/named/fixture.go.txt"
        self.assertFalse(source.with_suffix("").exists())
        original = source.read_bytes()
        observed = []
        scan = self.fake.scan

        def inspect(command, cwd):
            if cwd != self.root:
                destination = cwd / "named/fixture.go"
                self.assertTrue(destination.is_file())
                self.assertEqual(destination.read_bytes(), original)
                self.assertFalse((cwd / "named/fixture.go.txt").exists())
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

    def test_missing_named_fixture_data_still_scans_and_cleans(self):
        source = self.root / "scripts/testdata/gostyle-regression/named/fixture.go.txt"
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
        self.fake.calls.clear()
        self.args.version = "v0.26.2-0.20261009074109-a852d2403d81"
        self.fake.graph["Version"] = self.args.version
        self.assertEqual(self.invoke(), 1)
        self.assertIn("configured gostyle differs from the reviewed upstream receipt", self.output.getvalue())
        self.assertEqual(len(self.fake.calls), 1)
        self.assertFalse(self.scans())


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
        gate.verify_diagnostics(completed([], 3, stderr=line + "\n"), expected[:1], fixture)
        for invalid in ["", line + "\n" + line, line.replace(":6:2:", ":6:3:"), line.replace("anonymous/fixture_test.go", "valid/fixture.go")]:
            result = completed([], 3, stderr=invalid)
            with self.subTest(invalid=invalid), self.assertRaises(gate.GateError):
                gate.verify_diagnostics(result, expected[:1], fixture)


    def test_full_six_diagnostic_contract(self):
        expected = json.loads((HERE / "testdata/gostyle-regression/expected.json").read_text())
        self.assertEqual(len(expected), 6)
        self.assertEqual([item["column"] for item in expected if item["path"] == "multiple/fixture.go"], [16, 8])
        fixture = Path("/fixture")
        lines = [f"{fixture / item['path']}:{item['line']}:{item['column']}: {item['message']}" for item in expected]
        gate.verify_diagnostics(completed([], 3, stderr="\n".join(lines)), expected, fixture)
        for index, line in enumerate(lines):
            for mutated in [lines[:index] + lines[index+1:], lines + [line], lines[:index] + [line + "-wrong"] + lines[index+1:]]:
                result = completed([], 3, stderr="\n".join(mutated))
                with self.subTest(index=index), self.assertRaises(gate.GateError):
                    gate.verify_diagnostics(result, expected, fixture)

    def test_checksum_receipt_duplicates_and_malformed_rejected(self):
        for text in ["module version", "module version bad", "module version h1:one\nmodule version h1:one"]:
            with self.subTest(text=text), self.assertRaises(gate.GateError):
                gate.checksum_records(text)


if __name__ == "__main__":
    unittest.main()

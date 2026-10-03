#!/usr/bin/env python3
"""Exercise the combined runner's trust boundary with immutable Git fixtures."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


RUNNER = Path(__file__).with_name("check_reuse.py")
BASELINE = ".github/duplication-baseline.json"
SPEC = importlib.util.spec_from_file_location("check_reuse_under_test", RUNNER)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)

# These executable doubles report their actual checkout, source, and arguments.
# The tested runner still performs real Git validation and immutable extraction.
EVENT_CODE = '''
import json
import os
from pathlib import Path
import subprocess
import sys

def event(kind, root):
    root = root.resolve()
    record = {
        "kind": kind,
        "root": str(root),
        "revision": subprocess.check_output(
            [os.environ["TEST_REUSE_GIT"], "rev-parse", "HEAD"],
            cwd=root, text=True).strip(),
        "source": (root / "source.go").read_text(),
        "args": sys.argv[1:],
        "program": sys.argv[0],
        "environment": {key: value for key, value in os.environ.items()
                        if key.startswith(("GO", "GIT_", "LOPPER_DUPLICATION_"))
                        or key in ("GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN")},
    }
    with open(os.environ["TEST_REUSE_LOG"], "a") as output:
        output.write(json.dumps(record) + "\\n")
'''

DUPLICATION = EVENT_CODE + '''
root = Path.cwd()
# Emulate the protected checker's optional revision override, so an inherited
# override cannot go undetected even though the normal invocation omits it.
if os.environ.get("LOPPER_DUPLICATION_REVISION"):
    raise SystemExit(2)
event("duplication", root)
raise SystemExit(int(os.environ.get("TEST_DUPLICATION_EXIT", "0")))
'''

HELPER = EVENT_CODE + '''
root = Path(sys.argv[sys.argv.index("-root") + 1])
event("helper", root)
if "-legacy-advisory=false" not in sys.argv or any(
        argument.startswith("-exceptions") for argument in sys.argv):
    raise SystemExit(2)
raise SystemExit(int(os.environ.get("TEST_HELPER_EXIT", "0")))
'''

POLICY = '''
import contextlib
import os
from pathlib import Path
import subprocess
import tempfile

@contextlib.contextmanager
def isolated_checkout(repo, revision):
    # Match the protected policy's environment-based executable selection.
    git = os.environ.get("LOPPER_DUPLICATION_GIT", "git")
    with tempfile.TemporaryDirectory(prefix="reuse-source-fixture-") as directory:
        source = Path(directory) / "source"
        subprocess.run([git, "clone", "--no-local", "--no-checkout", "--", str(repo), str(source)],
                       check=True, capture_output=True)
        subprocess.run([git, "-c", "core.hooksPath=/dev/null", "fetch", "--no-tags", "origin", revision],
                       cwd=source, check=True, capture_output=True)
        subprocess.run([git, "-c", "core.hooksPath=/dev/null", "checkout", "--detach", revision],
                       cwd=source, check=True, capture_output=True)
        yield source
'''


class CombinedRunnerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="reuse-runner-test-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.root = self.directory / "repository"
        self.root.mkdir()
        self.bin = self.directory / "bin"
        self.bin.mkdir()
        self.log = self.directory / "events.jsonl"
        self.marker = self.directory / "candidate-executed"
        self.git_executable = shutil.which("git")
        self.assertIsNotNone(self.git_executable, "Git is required for immutable-source tests")
        self.environment = {
            key: value for key, value in os.environ.items()
            if not key.startswith(("GIT_", "GO", "LOPPER_DUPLICATION_"))
            and key not in ("GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN")
        }
        self.environment.update(
            PATH=str(self.bin) + os.pathsep + os.environ.get("PATH", os.defpath),
            GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
            GIT_CONFIG_SYSTEM=os.devnull, GIT_NO_REPLACE_OBJECTS="1",
            TEST_REUSE_GIT=self.git_executable, TEST_REUSE_LOG=str(self.log),
        )
        self.write("scripts/check_reuse.py", RUNNER.read_text())
        self.write("scripts/check_duplication.py", DUPLICATION)
        self.write("scripts/duplication_policy.py", POLICY)
        self.write("scripts/duplication_index.go", "// protected indexer\n")
        self.write("tools/reusecheck/main.go", "// protected helper\n")
        self.write("internal/reusecheck/check.go", "// protected implementation\n")
        self.write("Makefile", "GO ?= go\nDUPL_VERSION ?= " + "a" * 40
                   + "\nDUPLICATION_TOKEN_THRESHOLD ?= 55\nDUPLICATION_MAX ?= 3\n")
        self.write("go.mod", "module example.test/trusted\n\ngo 1.26\n")
        self.write("go.sum", "")
        self.write(BASELINE, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.write("source.go", 'package fixture\nconst Revision = "base"\n')
        build = EVENT_CODE + "\nHELPER = " + repr(HELPER) + '''
root = Path.cwd()
event("build", root)
if (root / "tools/reusecheck/main.go").read_text() != "// protected helper\\n":
    raise SystemExit(2)
if (root / "go.mod").read_text() != "module example.test/trusted\\n\\ngo 1.26\\n":
    raise SystemExit(2)
if os.environ.get("TEST_BUILD_EXIT"):
    raise SystemExit(int(os.environ["TEST_BUILD_EXIT"]))
binary = Path(sys.argv[sys.argv.index("-o") + 1])
binary.write_text("#!" + sys.executable + "\\n" + HELPER)
binary.chmod(0o755)
'''
        self.fake_go = self.bin / "go"
        self.fake_go.write_text("#!" + sys.executable + "\n" + build)
        self.fake_go.chmod(0o755)
        self.git("init", "-q", "-b", "target")
        self.git("config", "user.name", "Reuse Test")
        self.git("config", "user.email", "reuse-test@example.invalid")
        self.base = self.commit()

    def write(self, path, text):
        destination = self.root / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(text)

    def git(self, *arguments):
        return subprocess.check_output(
            [self.git_executable, "-c", "core.hooksPath=/dev/null", *arguments],
            cwd=self.root, env=self.environment, stderr=subprocess.STDOUT, text=True).strip()

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "immutable fixture")
        return self.git("rev-parse", "HEAD")

    def candidate(self, changes=None):
        if changes is None:
            changes = {"source.go": 'package fixture\nconst Revision = "candidate"\n'}
        for path, text in changes.items():
            self.write(path, text)
        revision = self.commit()
        self.git("checkout", "--detach", self.base)
        return revision

    def run_gate(self, revision, *, base=None, environment=None):
        result = subprocess.run(
            [sys.executable, "-E", "-S", "-B", str(self.root / "scripts/check_reuse.py"),
             "--base", self.base if base is None else base, "--revision", revision],
            cwd=self.directory, env=dict(self.environment, **(environment or {})),
            capture_output=True, text=True)
        return result

    def events(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def assert_exit(self, result, expected):
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_both_detectors_use_requested_commit_and_protected_tooling(self):
        attack = "from pathlib import Path\nPath(" + repr(str(self.marker)) + ").touch()\nraise SystemExit(0)\n"
        revision = self.candidate({
            "source.go": 'package fixture\nconst Revision = "candidate"\n',
            "Makefile": "all:\n\ttouch " + str(self.marker) + "\n",
            "go.mod": "module candidate.invalid/untrusted\n\ngo 999.0\n",
            "go.work": "go 999.0\nuse ./candidate-module\n",
            "scripts/check_reuse.py": attack,
            "scripts/check_duplication.py": attack,
            "scripts/duplication_policy.py": attack,
            "tools/reusecheck/main.go": "// untrusted candidate helper\n",
        })
        # Uncommitted candidate-looking source in the base worktree must not be
        # confused with the immutable source passed to either scanner.
        self.write("source.go", 'package fixture\nconst Revision = "worktree"\n')
        result = self.run_gate(revision)
        self.assert_exit(result, 0)
        build, duplicate, helper = self.events()
        self.assertEqual([build["kind"], duplicate["kind"], helper["kind"]],
                         ["build", "duplication", "helper"])
        self.assertEqual(build["revision"], self.base)
        self.assertEqual(Path(build["root"]), self.root)
        for detector in (duplicate, helper):
            self.assertEqual(detector["revision"], revision)
            self.assertIn('Revision = "candidate"', detector["source"])
            self.assertNotEqual(Path(detector["root"]), self.root)
        self.assertEqual(duplicate["root"], helper["root"])
        self.assertEqual(Path(duplicate["program"]), self.root / "scripts/check_duplication.py")
        self.assertEqual(duplicate["args"][duplicate["args"].index("--base") + 1], self.base)
        self.assertEqual(duplicate["args"][duplicate["args"].index("--version") + 1], "a" * 40)
        self.assertIn("-legacy-advisory=false", helper["args"])
        self.assertFalse(any(argument.startswith("-exceptions") for argument in helper["args"]))
        self.assertFalse(self.marker.exists(), "candidate-controlled code was executed")

    def test_empty_clone_families_and_exceptions_allow_a_valid_candidate(self):
        policy = json.loads((self.root / BASELINE).read_text())
        self.assertEqual(policy["families"], [])
        self.assertEqual(policy["exceptions"], [])
        revision = self.candidate()
        result = self.run_gate(revision)
        self.assert_exit(result, 0)
        self.assertEqual([event["kind"] for event in self.events()],
                         ["build", "duplication", "helper"])

    def test_no_relevant_changes_report_explicit_success_without_build(self):
        revision = self.candidate({"README.md": "Documentation only.\n"})
        result = self.run_gate(revision)
        self.assert_exit(result, 0)
        self.assertIn("no relevant source or policy changes", result.stdout)
        self.assertEqual(self.events(), [])

    def test_attribute_only_source_conversion_runs_both_detectors(self):
        source = (self.root / "source.go").read_text()
        self.write(".gitattributes", "*.go working-tree-encoding=UTF-16LE\n")
        # Stage only the attributes: re-adding source.go would renormalize its
        # blob, while this regression needs an unchanged blob whose checkout
        # bytes change because of the candidate's new attributes.
        self.git("add", ".gitattributes")
        self.git("commit", "-qm", "candidate checkout encoding")
        revision = self.git("rev-parse", "HEAD")
        self.assertEqual(self.git("diff", "--name-only", self.base, revision), ".gitattributes")
        self.git("checkout", "--detach", self.base)

        result = self.run_gate(revision, environment={"TEST_HELPER_EXIT": "2"})
        self.assert_exit(result, 2)
        events = self.events()
        self.assertEqual([event["kind"] for event in events],
                         ["build", "duplication", "helper"])
        for event in events[1:]:
            self.assertEqual(event["revision"], revision)
            self.assertEqual(event["source"].encode("utf-8"), source.encode("utf-16-le"))

    def test_both_detectors_run_and_failures_never_pass(self):
        revision = self.candidate()
        for duplicate, helper, expected in ((1, 0, 1), (0, 1, 1), (1, 1, 1), (2, 0, 2), (0, 2, 2), (1, 7, 2)):
            with self.subTest(duplicate=duplicate, helper=helper):
                self.log.unlink(missing_ok=True)
                result = self.run_gate(revision, environment={
                    "TEST_DUPLICATION_EXIT": str(duplicate), "TEST_HELPER_EXIT": str(helper)})
                self.assert_exit(result, expected)
                self.assertEqual([event["kind"] for event in self.events()],
                                 ["build", "duplication", "helper"])

    def test_build_failure_is_analysis_failure(self):
        result = self.run_gate(self.candidate(), environment={"TEST_BUILD_EXIT": "1"})
        self.assert_exit(result, 2)
        self.assertEqual([event["kind"] for event in self.events()], ["build"])

    def test_candidate_explicit_exception_is_rejected_before_scanning(self):
        revision = self.candidate({BASELINE: json.dumps({
            "version": 1, "families": [], "exceptions": [{"finding": "candidate approval"}]})})
        result = self.run_gate(revision)
        self.assert_exit(result, 2)
        self.assertIn("no-suppression", result.stderr)
        self.assertEqual([event["kind"] for event in self.events()], ["build"])

    def test_protected_explicit_exception_is_rejected_even_for_documentation(self):
        self.write(BASELINE, json.dumps({"version": 1, "families": [], "exceptions": [{}]}))
        self.base = self.commit()
        result = self.run_gate(self.candidate({"README.md": "Documentation.\n"}))
        self.assert_exit(result, 2)
        self.assertIn("no-suppression", result.stderr)
        self.assertEqual(self.events(), [])

    def test_candidate_historical_family_is_rejected_before_scanning(self):
        revision = self.candidate({BASELINE: json.dumps({
            "version": 1, "families": [{
                "members": ["source.go::first@a", "source.go::second@b"],
                "canonical_helper": None}], "exceptions": []})})
        result = self.run_gate(revision)
        self.assert_exit(result, 2)
        self.assertIn("no-suppression", result.stderr)
        self.assertEqual([event["kind"] for event in self.events()], ["build"])

    def test_protected_historical_family_blocks_documentation_and_noop(self):
        self.write(BASELINE, json.dumps({"version": 1, "families": [{
            "members": ["source.go::first@a", "source.go::second@b"],
            "canonical_helper": None}], "exceptions": []}))
        self.base = self.commit()
        documentation = self.candidate({"README.md": "Documentation.\n"})
        for revision in (self.base, documentation):
            with self.subTest(revision=revision):
                result = self.run_gate(revision)
                self.assert_exit(result, 2)
                self.assertIn("no-suppression", result.stderr)
                self.assertEqual(self.events(), [])

    def test_missing_protected_dependency_never_skips_to_success(self):
        (self.root / "tools/reusecheck/main.go").unlink()
        self.base = self.commit()
        result = self.run_gate(self.candidate({"README.md": "Documentation.\n"}))
        self.assert_exit(result, 2)
        self.assertIn("Missing protected reuse dependency", result.stderr)
        self.assertEqual(self.events(), [])

    def test_symlink_candidate_baseline_cannot_supply_policy(self):
        self.write("candidate-policy.json", (self.root / BASELINE).read_text())
        (self.root / BASELINE).unlink()
        (self.root / BASELINE).symlink_to("../candidate-policy.json")
        revision = self.commit()
        self.git("checkout", "--detach", self.base)
        result = self.run_gate(revision)
        self.assert_exit(result, 2)
        self.assertIn("symlink", result.stderr)

    def test_modified_protected_checker_is_rejected(self):
        revision = self.candidate()
        self.write("scripts/check_duplication.py", "raise SystemExit(0)\n")
        result = self.run_gate(revision)
        self.assert_exit(result, 2)
        self.assertEqual(self.events(), [])

    def test_untracked_and_ignored_protected_sources_are_rejected(self):
        self.write(".gitignore", "tools/reusecheck/ignored.go\n")
        self.base = self.commit()
        revision = self.candidate()
        for name in ("injected.go", "ignored.go"):
            with self.subTest(name=name):
                path = "tools/reusecheck/" + name
                self.write(path, 'package main\nfunc init() { panic("untracked code") }\n')
                if name == "ignored.go":
                    self.assertEqual(self.git("check-ignore", path), path)
                result = self.run_gate(revision)
                self.assert_exit(result, 2)
                self.assertIn("Untracked files", result.stderr)
                self.assertEqual(self.events(), [])
                (self.root / path).unlink()

    def test_index_flags_cannot_hide_modified_protected_policy(self):
        revision = self.candidate()
        path = "scripts/duplication_policy.py"
        attack = "from pathlib import Path\nPath(" + repr(str(self.marker)) + ").touch()\n"
        for flag in ("assume-unchanged", "skip-worktree"):
            with self.subTest(flag=flag):
                self.git("update-index", "--" + flag, path)
                try:
                    self.write(path, attack)
                    self.assertEqual(self.git("diff", "--exit-code", "HEAD", "--", path), "")
                    result = self.run_gate(revision)
                    self.assert_exit(result, 2)
                    self.assertIn("index flags", result.stderr)
                    self.assertEqual(self.events(), [])
                    self.assertFalse(self.marker.exists(), "modified protected policy was executed")
                finally:
                    self.write(path, POLICY)
                    self.git("update-index", "--no-" + flag, path)

    def test_non_go_symlink_referent_change_runs_both_detectors(self):
        (self.root / "source.go").unlink()
        self.write("source.txt", 'package fixture\nconst Revision = "base"\n')
        (self.root / "source.go").symlink_to("source.txt")
        self.base = self.commit()
        candidate_source = 'package fixture\nconst Revision = "candidate"\n'
        revision = self.candidate({"source.txt": candidate_source})
        result = self.run_gate(revision, environment={"TEST_HELPER_EXIT": "1"})
        self.assert_exit(result, 1)
        events = self.events()
        self.assertEqual([event["kind"] for event in events],
                         ["build", "duplication", "helper"])
        for event in events[1:]:
            self.assertEqual(event["revision"], revision)
            self.assertEqual(event["source"], candidate_source)

    def test_mutable_or_missing_revision_cannot_pass(self):
        for revision in ("HEAD", "-main", "A" * 40, "0" * 40):
            with self.subTest(revision=revision):
                result = self.run_gate(revision)
                self.assert_exit(result, 2)
        self.assertEqual(self.events(), [])

    def test_revision_must_include_selected_base(self):
        revision = self.base
        self.write("README.md", "New protected base.\n")
        self.base = self.commit()
        result = self.run_gate(revision)
        self.assert_exit(result, 2)
        self.assertEqual(self.events(), [])

    def test_inherited_git_go_credentials_and_revision_overrides_are_removed(self):
        revision = self.candidate()
        result = self.run_gate(revision, environment={
            "GIT_DIR": str(self.directory / "nonexistent"),
            "GOFLAGS": "-overlay=untrusted.json", "GOWORK": "untrusted.work",
            "GOENV": "untrusted.env", "GOCACHEPROG": "untrusted-cache-helper",
            "GOTOOLCHAIN": "untrusted-toolchain", "GOROOT": "untrusted-root",
            "LOPPER_DUPLICATION_REVISION": self.base,
            "LOPPER_DUPLICATION_GIT": str(self.directory / "untrusted-git"),
            "LOPPER_DUPLICATION_GO": str(self.directory / "untrusted-go"),
            "GH_TOKEN": "test-only-token", "GITHUB_TOKEN": "test-only-token",
            "ACTIONS_RUNTIME_TOKEN": "test-only-token",
        })
        self.assert_exit(result, 0)
        for event in self.events():
            environment = event["environment"]
            self.assertEqual(environment["GOFLAGS"], "")
            self.assertEqual(environment["GOWORK"], "off")
            self.assertEqual(environment["GOENV"], "off")
            self.assertEqual(environment["GOTOOLCHAIN"], "local")
            for forbidden in ("GIT_DIR", "GOCACHEPROG", "GOROOT", "LOPPER_DUPLICATION_REVISION",
                              "GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN"):
                self.assertNotIn(forbidden, environment)
            self.assertEqual(environment["LOPPER_DUPLICATION_GIT"], self.git_executable)


class BoundaryUnitTests(unittest.TestCase):
    def test_result_classification_is_fail_closed(self):
        self.assertEqual(runner.combine_results(0, 0), 0)
        for results in ((1, 0), (0, 1), (1, 1)):
            self.assertEqual(runner.combine_results(*results), 1)
        for results in ((-9, 0), (0, 2), (1, 127)):
            self.assertEqual(runner.combine_results(*results), 2)

    def test_policy_changes_are_relevant_including_deletions(self):
        for path in (BASELINE, "Makefile", "go.mod", "go.work", "CODEOWNERS",
                     ".gitattributes", "internal/.gitattributes", "internal/lang/.gitattributes",
                     ".github/workflows/reuse.yml", "scripts/check_reuse.py",
                     "scripts/duplication_policy.py", "internal/reusecheck/contracts.go",
                     "tools/reusecheck/main.go", "removed.go"):
            with self.subTest(path=path):
                self.assertTrue(runner.relevant([path]))
        self.assertFalse(runner.relevant(["README.md", "docs/usage.md"]))

    def test_truncated_path_list_is_analysis_failure(self):
        with mock.patch.object(runner, "checked", return_value="source.go"):
            with self.assertRaisesRegex(runner.AnalysisError, "Truncated"):
                runner.changed_paths(Path("."), "a" * 40, "b" * 40, "git", {})


if __name__ == "__main__":
    unittest.main()

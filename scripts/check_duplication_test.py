"""Isolated failure and comparison fixtures for the duplication runner."""

import contextlib
import io
import json
import os
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_duplication as runner


def historical_policy(pairs):
    return {'version': 1, 'families': [{'members': list(pair), 'canonical_helper': None} for pair in pairs], 'exceptions': []}


class DuplicationRunnerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name).resolve()
        self.environment = {key: value for key, value in os.environ.items()
                            if not key.startswith("GIT_") and key != "LOPPER_DUPLICATION_REVISION"}
        environment_patch = mock.patch.dict(os.environ, self.environment, clear=True)
        environment_patch.start()
        self.addCleanup(environment_patch.stop)
        self.git("init", "-q", "-b", "target")
        self.git("config", "user.name", "Ben Ranford")
        self.git("config", "user.email", "84072202+ben-ranford@users.noreply.github.com")
        self.git("config", "--local", "gc.auto", "0")
        self.git("config", "--local", "maintenance.auto", "false")
        self.write("Makefile", "GO ?= go\nDUPL_VERSION ?= pinned\nDUPLICATION_TOKEN_THRESHOLD ?= 55\n")
        self.write("original.go", "package fixture\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True, env=self.environment, stderr=subprocess.STDOUT)

    def write(self, name, content):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def commit(self):
        self.git("add", ".")
        self.git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "duplication fixture")

    def test_fixture_commit_does_not_launch_automatic_maintenance(self):
        trace = self.repo / ".git" / "fixture-commit-trace.json"
        config = self.repo / ".git" / "trace-defaults.cfg"
        config.write_text("[maintenance]\n\tauto = true\n[gc]\n\tauto = 0\n")
        self.write("original.go", "package fixture\nvar Updated = 1\n")
        with mock.patch.dict(self.environment, {"GIT_TRACE2_EVENT": str(trace),
                                                "GIT_CONFIG_GLOBAL": str(config), "GIT_CONFIG_NOSYSTEM": "1"}):
            self.commit()
        events = [json.loads(line) for line in trace.read_text().splitlines()]
        self.assertTrue(any(event.get("event") == "start" and "commit" in event.get("argv", [])
                            for event in events), "trace did not observe the fixture commit")
        housekeeping = [event.get("argv") for event in events
                        if event.get("event") == "child_start"
                        and any(argument in ("maintenance", "gc") for argument in event.get("argv", []))]
        self.assertEqual(housekeeping, [], "fixture commit launched automatic Git housekeeping")

    def test_target_merge_base_covers_all_branch_commits(self):
        self.git("checkout", "-qb", "feature")
        self.write("first change.go", "package fixture\nvar First = 1\n")
        self.commit()
        self.write("second.go", "package fixture\nvar Second = 2\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            base, policy_base, merge_base = runner.comparison_base(self.repo, "target", {})
            changed = runner.added_lines(self.repo, merge_base)
        self.assertEqual(base, "target")
        self.assertEqual(policy_base, self.base)
        self.assertEqual(merge_base, self.base)
        self.assertEqual(changed, {(name, line) for name in ("first change.go", "second.go") for line in (1, 2)})

    def test_forced_diff_color_preserves_added_lines(self):
        self.git("checkout", "-qb", "feature")
        self.write("new.go", "package fixture\nvar Value = 1\n")
        self.commit()
        self.git("config", "color.diff", "always")
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, self.base), {("new.go", 1), ("new.go", 2)})

    def test_changed_filenames_are_literal_pathspecs(self):
        self.git("checkout", "-qb", "feature")
        self.write("foo[1].go", "package fixture\nvar First = 1\n")
        self.write("foo1.go", "package fixture\nvar Second = 2\nvar Third = 3\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, self.base), {
                ("foo[1].go", 1), ("foo[1].go", 2),
                ("foo1.go", 1), ("foo1.go", 2), ("foo1.go", 3),
            })

    def test_renamed_go_files_only_count_added_lines(self):
        self.git("checkout", "-qb", "feature")
        self.write("original.go", "package fixture\nvar A = 1\nvar B = 2\nvar C = 3\nvar D = 4\n")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        self.git("mv", "original.go", "renamed.go")
        self.write("renamed.go", "package fixture\nvar A = 1\nvar B = 2\nvar C = 3\nvar D = 4\nvar Added = 5\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {("renamed.go", 6)})

    def test_renames_ignore_configured_exhaustive_detection_limit(self):
        originals = {}
        for index in range(3):
            originals[index] = "package fixture\n" + "".join(
                f"var File{index}Value{line} = {line}\n" for line in range(20)
            )
            self.write(f"before{index}.go", originals[index])
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        for index, content in originals.items():
            self.git("mv", f"before{index}.go", f"after{index}.go")
            self.write(f"after{index}.go", content + f"var Added{index} = 20\n")
        self.commit()
        self.git("config", "diff.renameLimit", "1")
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {
                (f"after{index}.go", 22) for index in originals
            })

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_regular_file_changed_to_symlink_preserves_lexical_path(self):
        self.write("alias.go", "package fixture\nvar Old = 1\n")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), set())
        findings = (
            "alias.go:1-1: duplicate of original.go:1-1\n"
            "original.go:1-1: duplicate of alias.go:1-1\n"
        )
        self.assertEqual(runner.parse_findings(findings, self.repo), {
            ("alias.go", 1), ("original.go", 1)
        })

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_symlink_paths_cannot_escape_repository(self):
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "outside.go"
            target.write_text("package fixture\n")
            (self.repo / "alias.go").symlink_to(target)
            with self.assertRaisesRegex(runner.AnalysisError, "escapes repository"):
                runner.supported_path("alias.go", self.repo)

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_changed_go_files_are_analyzed(self):
        self.git("checkout", "-qb", "feature")
        (self.repo / "typed.go").symlink_to("original.go")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "typed.go").unlink()
        self.write("typed.go", "package fixture\nvar Added = 1\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {("typed.go", 2)})

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_changed_symlink_counts_source_changes_below_first_line(self):
        self.write("alias.go", "package fixture\nvar Old = 1\n")
        self.write("original.go", "package fixture\nvar New = 2\nvar More = 3\n")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {
                ("alias.go", 2), ("alias.go", 3),
            })

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_change_reads_symlink_target_at_comparison_base(self):
        self.write("original.go", "package fixture\nvar Old = 1\n")
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        self.write("alias.go", "package fixture\nvar New = 2\n")
        self.write("original.go", "package fixture\nvar New = 2\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {
                ("alias.go", 2), ("original.go", 2),
            })

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_change_fails_closed_for_unresolved_old_source(self):
        for target in ("missing.go", "alias.go", "../outside.go"):
            with self.subTest(target=target):
                alias = self.repo / "alias.go"
                if alias.exists():
                    alias.unlink()
                alias.symlink_to(target)
                self.commit()
                base = self.git("rev-parse", "HEAD").strip()
                alias.unlink()
                self.write("alias.go", "package fixture\n")
                self.commit()
                with mock.patch.dict(os.environ, self.environment, clear=True):
                    with self.assertRaisesRegex(runner.AnalysisError, "Cannot resolve Go source"):
                        runner.added_lines(self.repo, base)

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_change_rejects_binary_source(self):
        self.write("alias.go", "package fixture\n\0")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            with self.assertRaisesRegex(runner.AnalysisError, "binary Go diff"):
                runner.added_lines(self.repo, base)

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_changed_source_uses_go_line_boundaries(self):
        self.write("alias.go", "package fixture\n// first\u2028second\nvar Old = 1\n")
        self.write("original.go", "package fixture\n// first\u2028second\nvar New = 2\n")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {("alias.go", 3)})

    @unittest.skipIf(os.name == "nt", "symlink fixture requires a Windows developer-mode setup")
    def test_type_changed_source_preserves_bare_carriage_returns(self):
        self.write("alias.go", "package fixture\n// first\rsecond\nvar Old = 1\n")
        self.write("original.go", "package fixture\n// first\rsecond\nvar New = 2\n")
        self.commit()
        base = self.git("rev-parse", "HEAD").strip()
        (self.repo / "alias.go").unlink()
        (self.repo / "alias.go").symlink_to("original.go")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, base), {("alias.go", 3)})

    def test_hunk_headers_only_start_after_lf(self):
        for separator in ("\u2028", "\r", "\v", "\f"):
            for fragment in ("@@ invalid", "@@ -0,0 +40,2 @@"):
                with self.subTest(separator=separator, fragment=fragment):
                    diff = f"@@ -1 +1 @@\n-// old\n+// new{separator}{fragment}\n"
                    self.assertEqual(runner.changed_hunk_lines(diff, "alias.go"), {("alias.go", 1)})

    def test_regular_diff_preserves_carriage_returns_in_comments(self):
        self.write("original.go", "package fixture\n// comment\r@@ -0,0 +40,2 @@\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, self.base), {("original.go", 2)})

    def test_finding_ranges_use_lf_line_counts(self):
        self.write("original.go", "package fixture\n// comment\rcontinued\n")
        with self.assertRaisesRegex(runner.AnalysisError, "Invalid detector line range"):
            runner.finding_location("original.go", 1, 3, self.repo, {})

    def test_missing_and_unrelated_bases_fail_with_recovery(self):
        self.git("checkout", "--orphan", "unrelated")
        self.write("unrelated.go", "package unrelated\n")
        self.commit()
        for base in ("does-not-exist", "target"):
            with self.subTest(base=base), mock.patch.dict(os.environ, self.environment, clear=True):
                with self.assertRaisesRegex(runner.AnalysisError, "Fetch the target.*No fallback"):
                    runner.comparison_base(self.repo, base, {})

    def test_binary_go_changes_are_incomplete_analysis(self):
        self.git("checkout", "-qb", "feature")
        self.write("binary.go", "package fixture\n\0")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True), self.assertRaisesRegex(runner.AnalysisError, "binary Go diff"):
            runner.added_lines(self.repo, self.base)

    def test_base_priority_uses_actual_pr_target(self):
        with mock.patch.object(runner, "checked", return_value=subprocess.CompletedProcess([], 0, self.base + "\n", "")) as checked:
            for explicit, environment, expected in (
                ("pinned", {"BASE_SHA": "event"}, "pinned"),
                ("", {"BASE_SHA": "event", "GITHUB_BASE_REF": "release"}, "event"),
                ("", {"GITHUB_BASE_REF": "release"}, "refs/remotes/origin/release"),
                ("", {"BASE_REF": "dev"}, "refs/remotes/origin/dev"),
                ("", {}, "origin/main"),
            ):
                with self.subTest(expected=expected):
                    self.assertEqual(runner.comparison_base(self.repo, explicit, environment)[0], expected)
                    self.assertIn(expected + "^{commit}", checked.call_args_list[-2].args[0])

    def test_base_arguments_reject_options_and_revision_expressions(self):
        for base in ("--help", "-c", "target;echo marker", "target\n", "HEAD~1", "HEAD^{tree}", "target:original.go", "rélease"):
            for requested, environment in ((base, {}), ("", {"BASE_SHA": base}), ("", {"GITHUB_BASE_REF": base}), ("", {"BASE_REF": base})):
                with self.subTest(base=base, environment=environment), mock.patch.object(runner.subprocess, "run") as run:
                    with self.assertRaisesRegex(runner.AnalysisError, "Unsupported comparison base.*No fallback"):
                        runner.comparison_base(self.repo, requested, environment)
                    run.assert_not_called()

    def test_base_arguments_accept_named_refs_and_commit_shas(self):
        self.git("branch", "release/v1.8.9-fix_1")
        for base in ("HEAD", self.base, "release/v1.8.9-fix_1", "refs/heads/release/v1.8.9-fix_1"):
            with self.subTest(base=base), mock.patch.dict(os.environ, self.environment, clear=True):
                self.assertEqual(runner.comparison_base(self.repo, base, {}), (base, self.base, self.base))

    def test_no_changes_are_distinct_from_no_matches(self):
        with mock.patch.dict(os.environ, self.environment, clear=True):
            self.assertEqual(runner.added_lines(self.repo, self.base), set())
        self.assertEqual(runner.parse_findings("", self.repo), set())

    def pair(self, first="dir with spaces/a.go", second="b.go"):
        for name in ("dir with spaces/a.go", "b.go"):
            self.write(name, "package fixture\nvar Value = 1\n")
        return f"{first}:1-2: duplicate of {second}:1-2\n{second}:1-2: duplicate of {first}:1-2\n"

    def test_valid_pairs_support_spaces_and_platform_separators(self):
        for path in ("dir with spaces/a.go", "./dir with spaces/a.go", ".\\dir with spaces\\a.go", str(self.repo / "dir with spaces/a.go")):
            with self.subTest(path=path):
                self.assertEqual(runner.parse_findings(self.pair(path), self.repo), {(name, line) for name in ("dir with spaces/a.go", "b.go") for line in (1, 2)})

    def test_detector_records_preserve_unicode_filename_separators(self):
        for separator in ("\u2028", "\u2029", "\x85"):
            with self.subTest(separator=separator):
                name = f"first{separator}second.go"
                self.write(name, "package fixture\n")
                output = f"{name}:1-1: duplicate of original.go:1-1\noriginal.go:1-1: duplicate of {name}:1-1\n"
                self.assertEqual(runner.parse_findings(output, self.repo), {(name, 1), ("original.go", 1)})

    def test_malformed_truncated_and_unsupported_records_fail(self):
        valid = self.pair()
        cases = [
            "nonsense\n", "\n", valid.rstrip("\n"), valid.splitlines()[0] + "\n",
            valid.replace(":1-2:", ":2-1:"), valid.replace(":1-2:", ":0-2:"),
            valid.replace(":1-2:", ":1-999:"), valid.replace("b.go", "missing.go"),
            valid.replace("b.go", "../outside.go"), valid.replace("b.go", "C:\\outside.go"),
            valid.replace("b.go", "bad:record.go"), valid.replace("b.go", "dir with spaces/a.go"),
        ]
        for output in cases:
            with self.subTest(output=output), self.assertRaises(runner.AnalysisError):
                runner.parse_findings(output, self.repo)

    def test_detector_crash_and_parse_diagnostics_fail(self):
        for status, stderr in ((1, "crashed"), (0, "parse error")):
            results = [subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], status, b"", stderr.encode())]
            with self.subTest(status=status), mock.patch.object(runner.subprocess, "run", side_effect=results), self.assertRaises(runner.AnalysisError):
                runner.scan(self.repo, "go", "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)

    def test_detector_arguments_reject_unpinned_versions_and_command_flags(self):
        for version in ("latest", "-toolexec=sh", "abc;touch marker", "a" * 39, "a" * 40 + "\n"):
            with self.subTest(version=version), mock.patch.object(runner.subprocess, "run") as run:
                with self.assertRaisesRegex(runner.AnalysisError, "pinned"):
                    runner.scan(self.repo, "go", version, 55)
                run.assert_not_called()
        with mock.patch.object(runner.subprocess, "run") as run, self.assertRaisesRegex(runner.AnalysisError, "Go executable"):
            runner.scan(self.repo, "go -toolexec=sh", "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)
        run.assert_not_called()

    def test_parser_rejects_long_ambiguous_records(self):
        with self.assertRaises(runner.AnalysisError):
            runner.parse_findings(("file:1-2:" * 10000) + "\n", self.repo)
        with self.assertRaises(runner.AnalysisError):
            runner.changed_hunk_lines("@@ invalid hunk", "file.go")

    def test_detector_install_failure_is_not_masked(self):
        with mock.patch.object(runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, b"", b"install failed")), self.assertRaisesRegex(runner.AnalysisError, "install failed"):
            runner.scan(self.repo, "go", "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)

    def test_cli_reports_no_change_success_and_duplicate_failure(self):
        command = [sys.executable, "-B", str(Path(runner.__file__).resolve()), "--version", "pinned", "--base", "target"]
        result = subprocess.run(command, cwd=self.repo, env=self.environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no changed Go lines", result.stdout)
        result = subprocess.run(command + ["--base", "missing"], cwd=self.repo, env=self.environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn("No fallback", result.stderr)
        with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "added_lines", return_value={("b.go", 1)}), mock.patch.object(runner, "scan", return_value={("b.go", 1)}), mock.patch.dict(os.environ, self.environment, clear=True), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(runner.main(["--version", "pinned", "--base", "target"]), 1)

    def test_clone_records_preserve_validated_endpoints(self):
        records = []
        runner.parse_findings(self.pair(), self.repo, records=records)
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0], (("dir with spaces/a.go", 1, 2), ("b.go", 1, 2)))

    def test_occurrence_gate_uses_target_policy_and_rejects_pr_expansion(self):
        empty = {"version": 1, "families": [], "exceptions": []}
        baseline_path = ".github/duplication-baseline.json"
        self.write(baseline_path, json.dumps(empty))
        self.commit()
        self.git("checkout", "-qb", "feature")
        self.write("added.go", "package fixture\n")
        self.commit()
        fn = {"path": "original.go", "name": "Original", "shape": "a", "start": 1, "end": 2}
        other = dict(fn, name="Copy", path="added.go")
        def detector(*args, records=None):
            records.append((("original.go", 1, 2), ("added.go", 1, 2)))
            return set()
        command = ["--version", "pinned", "--base", "target", "--baseline", baseline_path]
        with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "scan", side_effect=detector), mock.patch.object(runner.policy, "function_index", return_value=[fn, other]), mock.patch.dict(os.environ, self.environment, clear=True), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(runner.main(command), 1)
            proposal = historical_policy(runner.policy.clone_pairs([(("original.go", 1, 2), ("added.go", 1, 2))], [fn, other]))
            self.write(baseline_path, json.dumps(proposal))
            self.assertEqual(runner.main(command), 2)

    def test_stale_branch_cannot_bootstrap_baseline_after_target_approval(self):
        baseline_path = runner.CANONICAL_BASELINE
        self.write(baseline_path, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.write("Makefile", "GO ?= go\nDUPL_VERSION ?= pinned\nDUPLICATION_TOKEN_THRESHOLD ?= 56\n")
        self.commit()
        target_commit = self.git("rev-parse", "HEAD").strip()

        # Simulate a PR that forked before the target branch gained its policy.
        self.git("checkout", "-qb", "feature", self.base)
        self.write("added.go", "package fixture\n")
        self.commit()
        fn = {"path": "original.go", "name": "Original", "shape": "a", "start": 1, "end": 2}
        other = dict(fn, name="Copy", path="added.go")
        pair_records = [(('original.go', 1, 2), ('added.go', 1, 2))]
        proposal = historical_policy(runner.policy.clone_pairs(pair_records, [fn, other]))
        self.write(baseline_path, json.dumps(proposal))
        self.commit()

        def detector(*args, records=None):
            records.extend(pair_records)
            return set()

        command = ["--version", "pinned", "--threshold", "56", "--base", "target", "--baseline", baseline_path]
        stderr = io.StringIO()
        patches = (
            mock.patch.object(runner.Path, "cwd", return_value=self.repo),
            mock.patch.object(runner, "scan", side_effect=detector),
            mock.patch.object(runner.policy, "function_index", return_value=[fn, other]),
            mock.patch.dict(os.environ, self.environment, clear=True),
            contextlib.redirect_stderr(stderr),
        )
        with patches[0], patches[1], patches[2], patches[3], patches[4]:
            self.assertEqual(runner.main(command), 2)
        self.assertIn("not permitted", stderr.getvalue())
        self.assertNotEqual(target_commit, self.base)

    def test_occurrence_gate_scans_synthetic_merge_of_exact_base_and_pr_head(self):
        baseline_path = runner.CANONICAL_BASELINE
        self.git("checkout", "-qb", "feature", self.base)
        self.write("pr.go", "package fixture\nfunc Copy() {}\n")
        self.commit()
        head = self.git("rev-parse", "HEAD").strip()

        self.git("checkout", "target")
        self.write("base.go", "package fixture\nfunc Base() {}\n")
        self.write(baseline_path, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.commit()
        target = self.git("rev-parse", "HEAD").strip()
        merge_tree = self.git("merge-tree", "--write-tree", target, head).strip()
        synthetic = self.git("commit-tree", merge_tree, "-p", target, "-p", head, "-m", "prospective merge").strip()
        self.git("update-ref", "refs/heads/duplication-merge", synthetic)

        pair_records = [(('base.go', 1, 2), ('pr.go', 1, 2))]
        fn = {"path": "base.go", "name": "Base", "shape": "a", "start": 1, "end": 2}
        other = dict(fn, name="Copy", path="pr.go")

        def detector(repo, *args, records=None):
            self.assertTrue((repo / "base.go").is_file(), "target-side additions must be in the scanned tree")
            self.assertTrue((repo / "pr.go").is_file(), "PR-side additions must be in the scanned tree")
            records.extend(pair_records)
            return set()

        command = ["--version", "pinned", "--base", "target", "--baseline", baseline_path]
        environment = dict(self.environment, LOPPER_DUPLICATION_REVISION=synthetic)
        stdout, stderr = io.StringIO(), io.StringIO()
        patches = (
            mock.patch.object(runner.Path, "cwd", return_value=self.repo),
            mock.patch.object(runner, "scan", side_effect=detector),
            mock.patch.object(runner.policy, "function_index", return_value=[fn, other]),
            mock.patch.dict(os.environ, environment, clear=True),
            contextlib.redirect_stdout(stdout),
            contextlib.redirect_stderr(stderr),
        )
        with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5]:
            self.assertEqual(runner.main(command), 1, stderr.getvalue())
        self.assertIn("violation", stdout.getvalue())

    def test_trusted_analyzer_ignores_candidate_checker_policy_and_make_noops(self):
        baseline = runner.CANONICAL_BASELINE
        self.write(baseline, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.write("original.go", "package fixture\nfunc Original() {}\n")
        self.commit()
        self.git("checkout", "-qb", "feature")
        self.write("copy.go", "package fixture\nfunc Copy() {}\n")
        self.write("Makefile", "dup-check ci-checks:\n\t@true\n")
        for name in ("check_duplication.py", "duplication_policy.py"):
            self.write("scripts/" + name, "raise SystemExit(0)\n")
        self.write("scripts/duplication_index.go", 'package main; import "fmt"; func main() { fmt.Print("[]") }\n')
        self.write(".github/workflows/ci.yml", "jobs: {verify: {steps: [{run: true}]}}\n")
        self.commit()
        revision = self.git("rev-parse", "HEAD").strip()

        def detector(checkout, *args, records=None):
            self.assertNotEqual(checkout, self.repo)
            self.assertEqual((checkout / "copy.go").read_text(), "package fixture\nfunc Copy() {}\n")
            records.append((("original.go", 1, 2), ("copy.go", 1, 2)))
            return set()

        environment = dict(self.environment, LOPPER_DUPLICATION_REVISION=revision)
        command = ["--version", "pinned", "--base", "target", "--baseline", baseline]
        stdout, stderr = io.StringIO(), io.StringIO()
        # The caller supplies this trusted runner module. Mock only detector
        # transport; use its real indexer, source isolation and policy evaluator.
        with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "scan", side_effect=detector), mock.patch.dict(os.environ, environment, clear=True), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            self.assertEqual(runner.main(command), 1, stderr.getvalue())
        self.assertIn("Production clone pairs: 1; violations: 1", stdout.getvalue())
        self.assertIn("original.go:2 (Original)", stdout.getvalue())
        self.assertIn("copy.go:2 (Copy)", stdout.getvalue())

    def test_initial_policy_rejects_seed_allowances_and_reports_every_pair(self):
        self.git("checkout", "-qb", "feature")
        self.write("added.go", "package fixture\n")
        fn = {"path": "original.go", "name": "Original", "shape": "a", "start": 1, "end": 2}
        other = dict(fn, name="Copy", path="added.go")
        def detector(*args, records=None):
            records.append((("original.go", 1, 2), ("added.go", 1, 2)))
            return set()

        pairs = runner.policy.clone_pairs([(("original.go", 1, 2), ("added.go", 1, 2))], [fn, other])
        baseline = historical_policy(pairs)
        baseline_path = ".github/duplication-baseline.json"
        self.write(baseline_path, json.dumps(baseline))
        self.commit()
        command = ["--version", "pinned", "--base", "target", "--baseline", baseline_path]
        patches = (
            mock.patch.object(runner.Path, "cwd", return_value=self.repo),
            mock.patch.object(runner, "scan", side_effect=detector),
            mock.patch.object(runner.policy, "function_index", return_value=[fn, other]),
            mock.patch.dict(os.environ, self.environment, clear=True),
            contextlib.redirect_stdout(io.StringIO()),
        )
        stderr = io.StringIO()
        with patches[0], patches[1], patches[2], patches[3], patches[4], contextlib.redirect_stderr(stderr):
            result = runner.main(command)
            self.assertEqual(result, 2, stderr.getvalue())
            self.assertIn("not permitted", stderr.getvalue())
            self.write(baseline_path, json.dumps({"version": 1, "families": [], "exceptions": []}))
            self.assertEqual(runner.main(command), 1)

    def test_initial_baseline_symlink_cannot_replace_protected_policy(self):
        baseline_path = runner.CANONICAL_BASELINE
        self.write(baseline_path, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.commit()
        self.git("checkout", "-qb", "feature")
        attacker_path = "attacker-baseline.json"
        self.write(attacker_path, json.dumps({"version": 1, "families": [], "exceptions": []}))
        (self.repo / baseline_path).unlink()
        (self.repo / baseline_path).symlink_to(attacker_path)
        command = ["--version", "pinned", "--base", "target", "--baseline", baseline_path]
        stderr = io.StringIO()
        with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "scan", return_value=set()), mock.patch.object(runner.policy, "function_index", return_value=[]), mock.patch.dict(os.environ, self.environment, clear=True), contextlib.redirect_stderr(stderr):
            self.assertEqual(runner.main(command), 2)
        self.assertIn("must not be a symlink", stderr.getvalue())

    def test_explicitly_empty_baseline_is_rejected(self):
        stderr = io.StringIO()
        with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.dict(os.environ, self.environment, clear=True), contextlib.redirect_stderr(stderr):
            self.assertEqual(runner.main(["--version", "pinned", "--base", "target", "--baseline", ""]), 2)
        self.assertIn("protected baseline path", stderr.getvalue())

    def test_baseline_proposals_are_rejected_before_scanning_or_writing(self):
        for proposed in ('proposal.json', ''):
            command = ["--version", "pinned", "--base", "target", "--propose-baseline", proposed]
            stderr = io.StringIO()
            with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "scan") as scan, contextlib.redirect_stderr(stderr):
                self.assertEqual(runner.main(command), 2)
                scan.assert_not_called()
            self.assertIn("Baseline proposals are disabled", stderr.getvalue())
        self.assertFalse((self.repo / 'proposal.json').exists())

    def test_goleak_fixture_changes_are_not_excluded_from_changed_lines(self):
        self.git("checkout", "-qb", "feature")
        self.write("nested/goleak_test.go", "package fixture\nvar Leaks = 1\n")
        self.commit()
        self.assertEqual(runner.added_lines(self.repo, self.base), {
            ("nested/goleak_test.go", 1), ("nested/goleak_test.go", 2),
        })

    def test_occurrence_settings_must_match_protected_configuration(self):
        class Settings:
            baseline = ".github/duplication-baseline.json"
            go = "go"
            version = "pinned"
            threshold = 55

        runner.validate_occurrence_settings(self.repo, self.base, Settings())
        cases = (
            ("baseline", "attacker-baseline.json", "protected baseline path"),
            ("go", "./go", "Go command must match"),
            ("version", "changed-detector", "version must match"),
            ("threshold", 10000, "threshold must match"),
        )
        for attribute, value, message in cases:
            with self.subTest(attribute=attribute):
                changed = Settings()
                setattr(changed, attribute, value)
                with self.assertRaisesRegex(runner.AnalysisError, message):
                    runner.validate_occurrence_settings(self.repo, self.base, changed)

    @unittest.skipIf(os.name == "nt", "PATH wrapper fixture uses POSIX shell scripts")
    def test_make_gate_ignores_python_and_go_wrappers_added_after_capture(self):
        source = Path(runner.__file__).resolve().parent
        version = "f008fcf5e62793d38bda510ee37aab8b0c68e76c"
        self.write("Makefile", (source.parent / "Makefile").read_text())
        for name in ("check_duplication.py", "duplication_policy.py"):
            self.write("scripts/" + name, (source / name).read_text())
        self.write(runner.CANONICAL_BASELINE, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.commit()
        self.write("trusted/go", '#!/bin/sh\necho "trusted Go install reached" >&2\nexit 19\n')
        (self.repo / "trusted/go").chmod(0o755)
        # The wrappers reproduce tools-install populating GOPATH/bin after capture.
        self.write("bin/python3", '#!/bin/sh\nexit 0\n')
        self.write("bin/go", """#!/bin/sh
case "$1" in
  install) printf '#!/bin/sh\\nexit 0\\n' > "$GOBIN/dupl"; chmod +x "$GOBIN/dupl" ;;
  run) printf '[]\\n' ;;
esac
""")
        for name in ("go", "python3"):
            (self.repo / "bin" / name).chmod(0o755)
        environment = dict(self.environment,
                           PATH=str(self.repo / "bin") + os.pathsep + self.environment["PATH"],
                           DUPLICATION_PYTHON=sys.executable,
                           LOPPER_DUPLICATION_GO=str(self.repo / "trusted/go"))
        result = subprocess.run(["make", "dup-check", "DUPLICATION_BASE=target", "DUPL_VERSION=" + version],
                                cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("trusted Go install reached", result.stderr)

    @unittest.skipIf(os.name == "nt", "toolchain wrapper fixture uses POSIX shell scripts")
    def test_captured_go_cannot_delegate_to_untrusted_toolchain(self):
        source = Path(runner.__file__).resolve().parent
        self.write("Makefile", (source.parent / "Makefile").read_text())
        # Probe the real launcher under the recipe's environment without installing
        # a detector or relying on a deliberately poisoned build configuration.
        self.write("scripts/check_duplication.py", 'import os\ngo = os.environ["LOPPER_DUPLICATION_GO"]\nos.execv(go, [go, "version"])\n')
        wrapper = self.repo / "bin/go1.999.0"
        self.write("bin/go1.999.0", '#!/bin/sh\nif [ "$1" = version ]; then echo intercepted > "$TOOLCHAIN_MARKER"; fi\n')
        wrapper.chmod(0o755)
        marker = self.repo / "toolchain-intercepted"
        environment = dict(self.environment,
                           PATH=str(wrapper.parent) + os.pathsep + self.environment["PATH"],
                           DUPLICATION_PYTHON=sys.executable,
                           LOPPER_DUPLICATION_GO=str(Path(shutil.which("go")).resolve()),
                           TOOLCHAIN_MARKER=str(marker))
        result = subprocess.run(["make", "dup-check", "GO_TOOLCHAIN=go1.999.0",
                                 "HOST_GOOS=linux", "HOST_GOARCH=amd64"],
                                cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("go version go", result.stdout)
        self.assertFalse(marker.exists(), "captured Go delegated to the PATH wrapper")

    def test_captured_python_ignores_startup_injection(self):
        source = Path(runner.__file__).resolve().parent
        self.write("Makefile", (source.parent / "Makefile").read_text())
        for name in ("check_duplication.py", "duplication_policy.py"):
            self.write("scripts/" + name, (source / name).read_text())
        self.write("startup/sitecustomize.py", "import os; os._exit(0)\n")
        environment = dict(self.environment, DUPLICATION_PYTHON=sys.executable,
                           PYTHONPATH=str(self.repo / "startup"))
        result = subprocess.run(["make", "dup-check", "DUPLICATION_BASE=missing"],
                                cwd=self.repo, env=environment, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("Cannot compare requested base", result.stderr)

    def test_index_uses_protected_code_when_contributor_replaces_indexer(self):
        self.write("scripts/duplication_index.go", 'package main; import "fmt"; func main() { fmt.Print("[]") }\n')
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            functions = runner.policy.function_index(self.repo, "go", [(("original.go", 1, 2), ("original.go", 1, 2))])
        self.assertEqual([fn['name'] for fn in functions], ['Seen'])

    def test_new_and_staged_detector_endpoints_are_indexed(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        self.write("original.go", "package fixture\nfunc Original() {}\n")
        self.commit()
        for staged in (False, True):
            with self.subTest(staged=staged):
                self.write("new.go", "package fixture\nfunc Clone() {}\n")
                if staged:
                    self.git("add", "new.go")
                locations = []
                duplicated = runner.parse_findings(
                    "original.go:2-2: duplicate of new.go:2-2\n"
                    "new.go:2-2: duplicate of original.go:2-2\n", self.repo, records=locations)
                self.assertTrue(duplicated)
                functions = runner.policy.function_index(self.repo, "go", locations)
                self.assertEqual([fn['path'] for fn in functions], ['new.go', 'original.go'])
                self.assertEqual({fn['name'] for fn in functions}, {'Original', 'Clone'})
                current = runner.policy.clone_pairs(locations, functions)
                self.assertEqual(len(current), 1)
                self.assertEqual(runner.policy.evaluate(current, historical_policy({}))['findings'][0]['status'], 'violation')

    def test_index_ignores_environment_and_persisted_go_overlays(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        self.write("fake-index.go", 'package main\nimport "fmt"\nfunc main() { fmt.Println("[]") }\n')
        overlay = self.repo / "overlay.json"
        self.write("overlay.json", json.dumps({"Replace": {
            str(self.repo / "scripts/duplication_index.go"): str(self.repo / "fake-index.go")}}))
        flags = "-overlay=" + str(overlay)
        self.write("go-env", "GOFLAGS=" + flags + "\n")
        for settings in ({"GOFLAGS": flags, "GOENV": "off"},
                         {"GOENV": str(self.repo / "go-env")},
                         {"GOROOT": str(self.repo / "counterfeit-root")}):
            environment = dict(self.environment, GOTOOLCHAIN="local", **settings)
            if "GOFLAGS" not in settings:
                environment.pop("GOFLAGS", None)
            with self.subTest(settings=settings), mock.patch.dict(os.environ, environment, clear=True):
                functions = runner.policy.function_index(self.repo, str(Path(shutil.which("go")).resolve()), [(("original.go", 1, 2), ("original.go", 1, 2))])
            self.assertIn("Seen", [function["name"] for function in functions])

    @unittest.skipIf(os.name == "nt", "Git wrapper fixture uses POSIX shell scripts")
    def test_captured_git_ignores_path_wrapper_for_checker_and_index(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        trusted_git = shutil.which("git")
        expected = self.git("rev-parse", "HEAD").strip()
        self.write("bin/git", "#!/bin/sh\nexit 0\n")
        (self.repo / "bin/git").chmod(0o755)
        environment = dict(self.environment, LOPPER_DUPLICATION_GIT=trusted_git,
                           PATH=str(self.repo / "bin") + os.pathsep + self.environment["PATH"])
        with mock.patch.dict(os.environ, environment, clear=True):
            actual = runner.checked(["git", "rev-parse", "HEAD"], self.repo).stdout.strip()
            functions = runner.policy.function_index(self.repo, "go", [(("original.go", 1, 2), ("original.go", 1, 2))])
        self.assertEqual(actual, expected)
        self.assertIn("Seen", [function["name"] for function in functions])

    def test_git_repository_overrides_cannot_hide_indexed_source(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        empty_index = self.repo / "empty-index"
        subprocess.run(["git", "read-tree", "--empty"], cwd=self.repo,
                       env=dict(self.environment, GIT_INDEX_FILE=str(empty_index)), check=True)
        alternate = self.repo / "alternate"
        subprocess.run(["git", "init", "-q", str(alternate)], env=self.environment, check=True)
        cases = [
            {"GIT_INDEX_FILE": str(empty_index)},
            {"GIT_DIR": str(alternate / ".git"), "GIT_WORK_TREE": str(alternate)},
            {"GIT_WORK_TREE": str(alternate)},
            {"GIT_COMMON_DIR": str(alternate / ".git")},
            {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": str(alternate)},
        ]
        for overrides in cases:
            environment = dict(self.environment, **overrides)
            with self.subTest(overrides=overrides), mock.patch.dict(os.environ, environment, clear=True):
                root = runner.checked(["git", "rev-parse", "--show-toplevel"], self.repo).stdout.strip()
                functions = runner.policy.function_index(self.repo, "go", [(("original.go", 1, 2), ("original.go", 1, 2))])
                self.assertEqual(Path(root).resolve(), self.repo)
                self.assertIn("Seen", [function["name"] for function in functions])

    @unittest.skipIf(os.name == "nt", "authentication helper fixture uses executable scripts")
    def test_detector_install_disables_authentication_helpers(self):
        indexer = "package main\nfunc main() {}\n"
        self.write("scripts/duplication_index.go", indexer)
        helper = self.repo / "auth-helper"
        helper.write_text("#!" + sys.executable + "\n" +
                          "from pathlib import Path\n" +
                          "Path('auth-helper-ran').touch()\n" +
                          "Path('scripts/duplication_index.go').write_text('package main; func main() { println(\"[]\") }\\n')\n")
        helper.chmod(0o755)
        go = str(Path(shutil.which("go")).resolve())
        environment = dict(self.environment, GOAUTH=str(helper), GOTOOLCHAIN="local")
        with mock.patch.dict(os.environ, environment, clear=True):
            # Use the real pinned installation and fresh module cache: GOAUTH
            # commands execute before the first HTTPS fetch, even without a 401.
            findings = runner.scan(self.repo, go, "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)
        self.assertIsInstance(findings, set)
        self.assertFalse((self.repo / "auth-helper-ran").exists())
        self.assertEqual((self.repo / "scripts/duplication_index.go").read_text(), indexer)

    @unittest.skipIf(os.name == "nt", "cache helper fixture uses executable scripts")
    def test_cache_helper_cannot_rewrite_detector_or_index_sources(self):
        source = Path(runner.__file__).resolve().parent
        indexer = (source / "duplication_index.go").read_text()
        self.write("scripts/duplication_index.go", indexer)
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        helper = self.repo / "cache-helper"
        helper.write_text("#!" + sys.executable + "\n" + """
import base64, json, pathlib, sys
pathlib.Path("cache-helper-ran").touch()
pathlib.Path("scripts/duplication_index.go").write_text('package main; import "fmt"; func main() { fmt.Print("[]") }\\n')
print(json.dumps({"KnownCommands": ["get", "put", "close"]}), flush=True)
for line in sys.stdin:
    if not line.strip():
        continue
    request = json.loads(line)
    response = {"ID": request["ID"]}
    if request["Command"] == "get":
        response["Miss"] = True
    elif request["Command"] == "put":
        body = base64.b64decode(json.loads(next(line for line in sys.stdin if line.strip()))) if request.get("BodySize") else b""
        path = pathlib.Path("cache-" + str(request["ID"])).resolve()
        path.write_bytes(body)
        response["DiskPath"] = str(path)
    print(json.dumps(response), flush=True)
    if request["Command"] == "close":
        break
""")
        helper.chmod(0o755)
        go = str(Path(shutil.which("go")).resolve())
        environment = dict(self.environment, GOCACHEPROG=str(helper), GOTOOLCHAIN="local")
        real_run = subprocess.run
        def install_with_local_build(command, **kwargs):
            if command[1:2] == ["install"]:
                kwargs["input"] = b"[]"
                result = real_run([go, "run", "./scripts/duplication_index.go"], **kwargs)
                detector = Path(kwargs["env"]["GOBIN"]) / "dupl"
                detector.write_text("#!/bin/sh\nexit 0\n")
                detector.chmod(0o755)
                return result
            return real_run(command, **kwargs)
        for phase in ("detector", "index"):
            self.write("scripts/duplication_index.go", indexer)
            (self.repo / "cache-helper-ran").unlink(missing_ok=True)
            with self.subTest(phase=phase), mock.patch.dict(os.environ, environment, clear=True):
                if phase == "detector":
                    with mock.patch.object(runner.subprocess, "run", side_effect=install_with_local_build):
                        runner.scan(self.repo, go, "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)
                else:
                    functions = runner.policy.function_index(self.repo, go, [(("original.go", 1, 2), ("original.go", 1, 2))])
                    self.assertIn("Seen", [function["name"] for function in functions])
                self.assertFalse((self.repo / "cache-helper-ran").exists())
                self.assertEqual((self.repo / "scripts/duplication_index.go").read_text(), indexer)

    def test_global_smudge_filter_cannot_replace_isolated_indexer(self):
        source = Path(runner.__file__).resolve().parent
        original = (source / "duplication_index.go").read_text()
        self.write("scripts/duplication_index.go", original)
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        revision = self.git("rev-parse", "HEAD").strip()
        home = self.repo / "poisoned-home"
        home.mkdir()
        attributes = home / "attributes"
        attributes.write_text("scripts/duplication_index.go filter=counterfeit\n")
        smudge = home / "smudge"
        smudge.write_text("#!/bin/sh\ncat >/dev/null\nprintf 'package main; func main() { println(\"[]\") }\\n'\n")
        smudge.chmod(0o755)
        config = home / ".gitconfig"
        for key, value in (("core.attributesFile", str(attributes)), ("filter.counterfeit.smudge", str(smudge))):
            subprocess.run(["git", "config", "--file", str(config), key, value], env=self.environment, check=True)
        environment = dict(self.environment, HOME=str(home), XDG_CONFIG_HOME=str(home / "xdg"))
        with mock.patch.dict(os.environ, environment, clear=True), runner.policy.isolated_checkout(self.repo, revision) as checkout:
            self.assertEqual((checkout / "scripts/duplication_index.go").read_text(), original)
            functions = runner.policy.function_index(checkout, "go", [(("original.go", 1, 2), ("original.go", 1, 2))])
            self.assertIn("Seen", [function["name"] for function in functions])

    def test_replacement_objects_cannot_promote_head_to_protected_base(self):
        common_base = self.base
        self.write("protected.go", "package fixture\nfunc Protected() {}\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD").strip()
        # Divergent branches avoid a cycle when the replacement points at HEAD.
        self.git("checkout", "-qb", "feature", common_base)
        self.write("added.go", "package fixture\nfunc Added() {}\n")
        self.commit()
        head = self.git("rev-parse", "HEAD").strip()
        tree = self.git("rev-parse", self.base + "^{tree}").strip()
        replacement = self.git("commit-tree", tree, "-p", head, "-m", "counterfeit ancestry").strip()
        self.git("replace", self.base, replacement)
        # The raw Git operation reproduces the bypass before policy sanitization.
        self.assertEqual(self.git("merge-base", self.base, head).strip(), head)
        environment = dict(self.environment, LOPPER_DUPLICATION_REVISION=head)
        with mock.patch.dict(os.environ, environment, clear=True):
            _, _, merge_base = runner.comparison_base(self.repo, "target", environment)
        self.assertEqual(merge_base, common_base)

    def test_isolated_checkout_preserves_detached_synthetic_merge_from_linked_worktree(self):
        self.git("checkout", "-qb", "feature")
        self.write("branch.go", "package fixture\nfunc Branch() {}\n")
        self.commit()
        branch = self.git("rev-parse", "HEAD").strip()
        tree = self.git("rev-parse", "HEAD^{tree}").strip()
        revision = self.git("commit-tree", tree, "-p", self.base, "-p", branch, "-m", "synthetic PR merge").strip()
        self.git("checkout", "--detach", revision)
        self.assertEqual(self.git("for-each-ref", "--contains", revision, "refs/heads").strip(), "")
        linked = self.repo / "linked-checkout"
        self.git("worktree", "add", "--detach", str(linked), revision)
        self.assertTrue((linked / ".git").is_file())
        with mock.patch.dict(os.environ, self.environment, clear=True):
            for source in (self.repo, linked):
                with self.subTest(source=source), runner.policy.isolated_checkout(source, revision) as checkout:
                    actual = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=checkout, env=self.environment, text=True).strip()
                    self.assertEqual(actual, revision)
                    self.assertEqual((checkout / "branch.go").read_text(), "package fixture\nfunc Branch() {}\n")
                    self.assertTrue((checkout / ".git").is_dir())

    def test_isolated_checkout_fetches_head_only_referenced_by_remote_tracking_ref(self):
        self.git("checkout", "-qb", "feature")
        self.write("branch.go", "package fixture\nfunc Branch() {}\n")
        self.commit()
        revision = self.git("rev-parse", "HEAD").strip()
        self.git("update-ref", "refs/remotes/origin/duplication-head", revision)
        self.git("checkout", "--detach", self.base)
        self.git("branch", "-D", "feature")
        self.assertEqual(self.git("for-each-ref", "--format=%(refname)", "refs/heads/feature"), "")
        self.assertEqual(self.git("rev-parse", "refs/remotes/origin/duplication-head").strip(), revision)

        with mock.patch.dict(os.environ, self.environment, clear=True), runner.policy.isolated_checkout(self.repo, revision) as checkout:
            actual = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=checkout, env=self.environment, text=True).strip()
            self.assertEqual(actual, revision)
            self.assertEqual((checkout / "branch.go").read_text(), "package fixture\nfunc Branch() {}\n")

    def test_empty_real_index_cannot_hide_committed_functions(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        self.write("original.go", "package fixture\nfunc Seen() {}\n")
        self.commit()
        self.git("read-tree", "--empty")
        with mock.patch.dict(os.environ, self.environment, clear=True):
            functions = runner.policy.function_index(self.repo, "go", [(("original.go", 1, 2), ("original.go", 1, 2))])
        self.assertIn("Seen", [function["name"] for function in functions])

    def test_tool_install_mutations_are_isolated_from_checked_occurrences(self):
        source = Path(runner.__file__).resolve().parent
        self.write("scripts/duplication_index.go", (source / "duplication_index.go").read_text())
        baseline = ".github/duplication-baseline.json"
        self.write(baseline, json.dumps({"version": 1, "families": [], "exceptions": []}))
        self.write("original.go", "package fixture\nfunc Original() {}\n")
        self.commit()
        self.git("checkout", "-qb", "feature")
        self.write("copy.go", "package fixture\nfunc Copy() {}\n")
        makefile = (self.repo / "Makefile").read_text()
        self.write("Makefile", makefile + "tools-install:\n\tgit read-tree --empty\n\tprintf 'package fixture\\n' > original.go\n")
        self.commit()
        revision = self.git("rev-parse", "HEAD").strip()
        index_before = (self.repo / ".git/index").read_bytes()
        source_before = (self.repo / "original.go").read_bytes()
        environment = dict(self.environment, LOPPER_DUPLICATION_REVISION=revision)
        with mock.patch.dict(os.environ, environment, clear=True):
            with runner.policy.isolated_checkout(self.repo, revision) as tooling:
                subprocess.run(["make", "tools-install"], cwd=tooling, env=self.environment,
                               capture_output=True, check=True)
                self.assertEqual(subprocess.check_output(["git", "ls-files"], cwd=tooling, env=self.environment), b"")
            self.assertEqual((self.repo / ".git/index").read_bytes(), index_before)
            self.assertEqual((self.repo / "original.go").read_bytes(), source_before)
            # Even a subsequently corrupted caller index/source cannot alter the captured scan.
            self.git("read-tree", "--empty")
            self.write("original.go", "package fixture\n")
            def detector(checkout, *args, records=None):
                self.assertNotEqual(checkout, self.repo)
                self.assertEqual((checkout / "original.go").read_bytes(), source_before)
                records.extend([(("original.go", 1, 2), ("copy.go", 1, 2))])
                return set()
            with mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.object(runner, "scan", side_effect=detector), contextlib.redirect_stdout(io.StringIO()):
                result = runner.main(["--version", "pinned", "--base", "target", "--baseline", baseline,
                                      "--report", ".github/report.json"])
            self.assertEqual(result, 1)
            report = json.loads((self.repo / ".github/report.json").read_text())
            self.assertEqual(len(report["findings"]), 1)
            self.assertEqual(report["findings"][0]["status"], "violation")

    def test_counterfeit_proxy_and_module_cache_cannot_supply_detector(self):
        commit = "f008fcf5e62793d38bda510ee37aab8b0c68e76c"
        version = "v0.0.0-20230101000000-" + commit[:12]
        module = "github.com/mibk/dupl"
        proxy = self.repo / "proxy"
        versions = proxy / module / "@v"
        versions.mkdir(parents=True)
        manifest = "module " + module + "\ngo 1.20\n"
        metadata = json.dumps({"Version": version, "Time": "2023-01-01T00:00:00Z"})
        for name in (commit, version):
            (versions / (name + ".info")).write_text(metadata)
        (versions / "list").write_text(version + "\n")
        (versions / (version + ".mod")).write_text(manifest)
        with zipfile.ZipFile(versions / (version + ".zip"), "w") as archive:
            archive.writestr(module + "@" + version + "/go.mod", manifest)
            archive.writestr(module + "@" + version + "/main.go", "package main\nfunc main() {}\n")
        cache = self.repo / "poisoned-cache"
        environment = dict(self.environment, GOPROXY=proxy.as_uri(), GOSUMDB="off", GOENV="off",
                           GOFLAGS="", GOTOOLCHAIN="local", GOMODCACHE=str(cache),
                           GOBIN=str(self.repo / "poisoned-bin"))
        go = str(Path(shutil.which("go")).resolve())
        # Seed a real counterfeit module cache using only the local file proxy.
        seeded = subprocess.run([go, "install", module + "@" + commit], cwd=self.repo,
                                env=environment, capture_output=True, text=True)
        self.assertEqual(seeded.returncode, 0, seeded.stderr)
        real_run = subprocess.run

        def offline_download(command, **kwargs):
            settings = kwargs.get("env", {})
            if len(command) > 1 and command[1] == "install" and settings.get("GOPROXY") == "https://proxy.golang.org":
                # The fixture never contacts the network. Authenticated downloads
                # are deliberately blocked; counterfeit local installs run normally.
                return subprocess.CompletedProcess(command, 1, b"", b"trusted download blocked by fixture")
            return real_run(command, **kwargs)

        for source_kind in ("proxy", "cache"):
            if source_kind == "cache":
                # Only metadata remains in the proxy; the counterfeit source must
                # now come from the already-populated module cache.
                (versions / (version + ".zip")).unlink()
            poisoned = dict(environment, GOMODCACHE=str(cache if source_kind == "cache" else self.repo / "fresh-cache"))
            with self.subTest(source=source_kind), mock.patch.dict(os.environ, poisoned, clear=True), mock.patch.object(runner.subprocess, "run", side_effect=offline_download):
                with self.assertRaisesRegex(runner.AnalysisError, "trusted download blocked by fixture"):
                    runner.scan(self.repo, go, commit, 55)

    def test_detector_install_rejects_inherited_module_trust(self):
        overrides = dict(GOPROXY="file:///attacker/proxy", GOSUMDB="off", GONOSUMDB="*",
                         GONOPROXY="*", GOPRIVATE="*", GOINSECURE="*", GOMODCACHE="/attacker/cache")
        results = [subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], 0, b"", b"")]
        with mock.patch.dict(os.environ, overrides), mock.patch.object(runner.subprocess, "run", side_effect=results) as run:
            runner.scan(self.repo, "go", "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)
        environment = run.call_args_list[0].kwargs["env"]
        self.assertEqual(environment["GOPROXY"], "https://proxy.golang.org")
        self.assertEqual(environment["GOSUMDB"], "sum.golang.org")
        for name in ("GONOSUMDB", "GONOPROXY", "GOPRIVATE", "GOINSECURE"):
            self.assertEqual(environment[name], "")
        self.assertEqual(Path(environment["GOMODCACHE"]).parent, Path(environment["GOBIN"]))
        self.assertFalse(Path(environment["GOMODCACHE"]).exists(), "temporary download cache was retained")

    def test_detector_install_ignores_go_environment_overrides(self):
        results = [subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], 0, b"", b"")]
        with mock.patch.dict(os.environ, GOFLAGS="-overlay=attacker.json", GOENV="attacker-env", GOROOT="attacker-root"), mock.patch.object(runner.subprocess, "run", side_effect=results) as run:
            runner.scan(self.repo, "go", "f008fcf5e62793d38bda510ee37aab8b0c68e76c", 55)
        environment = run.call_args_list[0].kwargs["env"]
        self.assertEqual(environment["GOFLAGS"], "")
        self.assertEqual(environment["GOENV"], "off")
        self.assertIsNone(environment.get("GOROOT"))

    def test_trusted_go_path_is_used_for_both_detector_and_index(self):
        class Settings:
            baseline = None
            propose_baseline = None
            go = "go"
            version = "pinned"
            threshold = 55

        trusted = str(self.repo / "trusted/go")
        with mock.patch.dict(os.environ, LOPPER_DUPLICATION_GO=trusted), mock.patch.object(runner, "scan") as scan, mock.patch.object(runner.policy, "function_index", return_value=[]) as index:
            self.assertEqual(runner.occurrence_gate(self.repo, self.base, Settings()), 0)
            self.assertEqual(scan.call_args.args[1], trusted)
            index.assert_called_once_with(self.repo, trusted, [])
        for invalid in ("", "go", "./go"):
            settings = Settings()
            with self.subTest(invalid=invalid), mock.patch.dict(os.environ, LOPPER_DUPLICATION_GO=invalid), mock.patch.object(runner, "scan") as scan:
                with self.assertRaisesRegex(runner.AnalysisError, "absolute path"):
                    runner.occurrence_gate(self.repo, self.base, settings)
                scan.assert_not_called()

    def test_cli_rejects_pr_selected_baseline_and_weaker_detector(self):
        cases = (
            (["--baseline", "attacker-baseline.json"], "protected baseline path"),
            (["--go", "./go", "--baseline", runner.CANONICAL_BASELINE], "Go command must match"),
            (["--threshold", "10000", "--baseline", runner.CANONICAL_BASELINE], "threshold must match"),
            (["--version", "changed-detector", "--baseline", runner.CANONICAL_BASELINE], "version must match"),
        )
        for options, message in cases:
            command = ["--base", "target", "--version", "pinned", *options]
            stderr = io.StringIO()
            with self.subTest(options=options), mock.patch.object(runner.Path, "cwd", return_value=self.repo), mock.patch.dict(os.environ, self.environment, clear=True), contextlib.redirect_stderr(stderr):
                self.assertEqual(runner.main(command), 2)
                self.assertIn(message, stderr.getvalue())

    def test_policy_paths_cannot_escape_the_repository(self):
        for value in ("../outside.json", str(self.repo.parent / "outside.json")):
            with self.subTest(value=value), self.assertRaisesRegex(runner.AnalysisError, "within the repository"):
                runner.repository_path(self.repo, value, "Baseline")
        outside = Path(self.temp.name).parent / f"{Path(self.temp.name).name}-outside"
        link = self.repo / "outside-link"
        link.symlink_to(outside)
        with self.assertRaisesRegex(runner.AnalysisError, "within the repository"):
            runner.repository_path(self.repo, "outside-link/escape.json", "Report")

    def test_invalid_threshold_cannot_disable_gate(self):
        for option, value in (("--max", "nan"), ("--max", "inf"), ("--max", "-1"), ("--threshold", "0")):
            with self.subTest(value=value), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.main(["--version", "pinned", option, value]), 2)


if __name__ == "__main__":
    unittest.main()

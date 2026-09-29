"""Isolated failure and comparison fixtures for the duplication runner."""

import contextlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_duplication as runner


class DuplicationRunnerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name).resolve()
        self.environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        self.git("init", "-q", "-b", "target")
        self.git("config", "user.name", "Ben Ranford")
        self.git("config", "user.email", "84072202+ben-ranford@users.noreply.github.com")
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

    def test_target_merge_base_covers_all_branch_commits(self):
        self.git("checkout", "-qb", "feature")
        self.write("first change.go", "package fixture\nvar First = 1\n")
        self.commit()
        self.write("second.go", "package fixture\nvar Second = 2\n")
        self.commit()
        with mock.patch.dict(os.environ, self.environment, clear=True):
            base, merge_base = runner.comparison_base(self.repo, "target", {})
            changed = runner.added_lines(self.repo, merge_base)
        self.assertEqual(base, "target")
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
                self.assertEqual(runner.comparison_base(self.repo, base, {}), (base, self.base))

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

    def test_invalid_threshold_cannot_disable_gate(self):
        for option, value in (("--max", "nan"), ("--max", "inf"), ("--max", "-1"), ("--threshold", "0")):
            with self.subTest(value=value), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.main(["--version", "pinned", option, value]), 2)


if __name__ == "__main__":
    unittest.main()

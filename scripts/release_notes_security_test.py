#!/usr/bin/env python3
"""Exercise release-note CLI validation at the subprocess boundary."""

import contextlib
import io
import runpy
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import vscode_release_notes


INVALID_TAGS = (
    "", "--help", "--output=unexpected", "-c core.pager=unexpected",
    "v1.0.0; touch unexpected", "v1.0.0$(id)", "v1.0.0`id`",
    "v1.0.0\n", " v1.0.0", "v1.0.0 ", "v1.0.0:go.mod",
    "v1.0.0..HEAD", "v1.0.0^{commit}", "v1.0.0-rc.1",
    "v1000000000.0.0", "v1.0.0\x00",
)


class ReleaseNotesSecurityTest(unittest.TestCase):
    def test_cli_rejects_invalid_tags_before_starting_any_subprocess(self):
        scripts = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as repo:
            for script in ("release_build_notes.py", "vscode_release_notes.py"):
                for tag in INVALID_TAGS:
                    with self.subTest(script=script, tag=tag):
                        stderr = io.StringIO()
                        argv = [script, "--repo", repo, f"--previous-tag={tag}"]
                        with mock.patch.object(sys, "argv", argv), contextlib.redirect_stderr(stderr):
                            with mock.patch.object(subprocess, "Popen") as popen:
                                with self.assertRaises(SystemExit) as result:
                                    runpy.run_path(str(scripts / script), run_name="__main__")
                                popen.assert_not_called()
                        self.assertEqual(result.exception.code, 1)
                        expected = vscode_release_notes.INVALID_STABLE_TAG
                        if script == "vscode_release_notes.py" and not tag:
                            expected = "--previous-tag is required"
                        self.assertIn(expected, stderr.getvalue())

    def test_exported_tag_readers_reject_invalid_tags_before_subprocess(self):
        for reader in (vscode_release_notes.old_lockfile, vscode_release_notes.old_package):
            for tag in INVALID_TAGS:
                with self.subTest(reader=reader.__name__, tag=tag):
                    with mock.patch.object(subprocess, "Popen") as popen:
                        with self.assertRaisesRegex(ValueError, vscode_release_notes.INVALID_STABLE_TAG):
                            reader(Path("."), tag)
                        popen.assert_not_called()

    def test_valid_tag_readers_preserve_git_show_arguments(self):
        repo = Path("repository with spaces")
        for reader, path in (
            (vscode_release_notes.old_lockfile, vscode_release_notes.LOCKFILE_PATH),
            (vscode_release_notes.old_package, vscode_release_notes.PACKAGE_PATH),
        ):
            with self.subTest(reader=reader.__name__):
                with mock.patch.object(subprocess, "check_output", return_value='{"version":"1.0.0"}') as output:
                    self.assertEqual(reader(repo, "v1.0.0"), {"version": "1.0.0"})
                output.assert_called_once_with(
                    ["git", "-C", str(repo), "show", f"v1.0.0:{path.as_posix()}"], text=True,
                )

    def test_subprocess_probe_observes_git_execution(self):
        with mock.patch.object(subprocess, "Popen", side_effect=RuntimeError("subprocess probe")) as popen:
            with self.assertRaisesRegex(RuntimeError, "subprocess probe"):
                vscode_release_notes.git(Path("."), "status")
            popen.assert_called_once()


if __name__ == "__main__":
    unittest.main()

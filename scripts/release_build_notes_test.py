#!/usr/bin/env python3

import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import release_build_notes


def fixture_git_env() -> dict[str, str]:
    environment = os.environ.copy()
    for key in tuple(environment):
        if key.startswith("GIT_"):
            del environment[key]
    return environment


class ReleaseBuildNotesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp.name)
        self.git("init", "-q", self.repo)
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.com")
        self.write("1.27.0", "1.0.0", self.changelog("1.0.0", "* Historical note.\n"))
        self.commit("initial release")
        self.git("tag", "v1.0.0")
        self.write("1.27.1", "1.0.1", self.changelog("1.0.1", "* Current note.\n", "1.0.0", "* Historical note.\n"))

    def tearDown(self) -> None:
        self.temp.cleanup()

    def changelog(self, version, body, older_version=None, older_body=None) -> str:
        output = "# Changelog\n\n"
        output += f"## [{version}](x) (2026-01-02)\n\n{body}\n"
        if older_version:
            output += f"## [{older_version}](x) (2026-01-01)\n\n{older_body}\n"
        return output

    def write(self, go_version, release_version, changelog, *, toolchain=None) -> None:
        go_mod = f"module example.invalid/test\n\ngo {go_version}\n"
        if toolchain:
            go_mod += f"toolchain {toolchain}\n"
        (self.repo / "go.mod").write_text(go_mod, encoding="utf-8")
        (self.repo / ".release-please-manifest.json").write_text(json.dumps({".": release_version}), encoding="utf-8")
        (self.repo / "CHANGELOG.md").write_text(changelog, encoding="utf-8")

    def commit(self, subject) -> None:
        self.git("add", ".")
        self.git("commit", "-qm", subject)

    def git(self, *args) -> None:
        if args[0] == "init":
            command = ["git", *args]
        else:
            command = ["git", "-C", self.repo, *args]
        subprocess.run(command, check=True, env=fixture_git_env())

    def generate(self) -> str:
        release_build_notes.generate(self.repo, "v1.0.0")
        return (self.repo / "CHANGELOG.md").read_text(encoding="utf-8")

    def test_commit_ignores_inherited_git_index(self) -> None:
        with tempfile.TemporaryDirectory() as marker_temp:
            marker_repo = Path(marker_temp)
            self.git("init", "-q", marker_repo)
            marker_file = marker_repo / "marker.txt"
            marker_file.write_text("marker\n", encoding="utf-8")
            subprocess.run(["git", "-C", marker_repo, "add", "marker.txt"], check=True, env=fixture_git_env())
            marker_index = marker_repo / ".git" / "index"
            before = marker_index.read_bytes()

            with mock.patch.dict("os.environ", {"GIT_INDEX_FILE": str(marker_index)}):
                self.commit("ignore inherited index")

            self.assertEqual(marker_index.read_bytes(), before)

    def test_adds_changed_patch_requirement_and_preserves_history(self) -> None:
        before = (self.repo / "CHANGELOG.md").read_text(encoding="utf-8")
        output = self.generate()
        self.assertIn("* Source builds require Go `1.27.1` or newer (previously `1.27.0`).", output)
        self.assertEqual(output[output.index("## [1.0.0]"):], before[before.index("## [1.0.0]"):])

    def test_adds_changed_minor_requirement(self) -> None:
        self.write("1.28.0", "1.0.1", (self.repo / "CHANGELOG.md").read_text(encoding="utf-8"))
        self.assertIn("Go `1.28.0` or newer (previously `1.27.0`)", self.generate())

    def test_places_requirement_before_release_please_categories(self) -> None:
        categorized = self.changelog(
            "1.0.1",
            "### Bug Fixes\n\n* A bug fix.\n\n### Code Refactoring\n\n* A refactor.\n",
            "1.0.0",
            "* Historical note.\n",
        )
        self.write("1.27.1", "1.0.1", categorized)

        output = self.generate()
        note = "* Source builds require Go `1.27.1` or newer (previously `1.27.0`)."
        self.assertLess(output.index(note), output.index("### Bug Fixes"))
        self.assertIn("### Bug Fixes\n\n* A bug fix.", output)
        self.assertIn("### Code Refactoring\n\n* A refactor.", output)
        self.assertEqual(output[output.index("## [1.0.0]"):], categorized[categorized.index("## [1.0.0]"):])

    def test_omits_unchanged_or_toolchain_only_requirement(self) -> None:
        self.write("1.27.0", "1.0.1", (self.repo / "CHANGELOG.md").read_text(encoding="utf-8"), toolchain="go1.27.1")
        self.assertNotIn("Source builds require Go", self.generate())

    def test_regeneration_refreshes_and_removes_stale_generated_note(self) -> None:
        self.generate()
        first = (self.repo / "CHANGELOG.md").read_text(encoding="utf-8")
        self.assertEqual(self.generate(), first)
        self.write("1.28.0", "1.0.1", first)
        self.assertIn("Go `1.28.0` or newer (previously `1.27.0`)", self.generate())
        self.write("1.27.0", "1.0.1", (self.repo / "CHANGELOG.md").read_text(encoding="utf-8"))
        self.assertNotIn("Source builds require Go", self.generate())

    def test_rejects_invalid_tag_missing_or_malformed_go_directive(self) -> None:
        with self.assertRaisesRegex(ValueError, "stable"):
            release_build_notes.generate(self.repo, "v1.0.0; unsafe")
        (self.repo / "go.mod").write_text("module example.invalid/test\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "go directive"):
            self.generate()
        (self.repo / "go.mod").write_text("module example.invalid/test\n\ngo invalid\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "go directive"):
            self.generate()

    def test_rejects_manifest_mismatch_and_unsafe_changelog_path(self) -> None:
        (self.repo / ".release-please-manifest.json").write_text('{".": "9.9.9"}', encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "newest entry"):
            self.generate()
        self.write("1.27.1", "1.0.1", (self.repo / "CHANGELOG.md").read_text(encoding="utf-8"))
        with tempfile.TemporaryDirectory() as outside_dir:
            outside = Path(outside_dir) / "CHANGELOG.md"
            outside.write_text("untouched\n", encoding="utf-8")
            (self.repo / "CHANGELOG.md").unlink()
            (self.repo / "CHANGELOG.md").symlink_to(outside)
            with self.assertRaisesRegex(ValueError, "outside"):
                self.generate()
            with self.assertRaisesRegex(ValueError, "outside"):
                release_build_notes.write_changelog(self.repo, "replacement\n")
            self.assertEqual(outside.read_text(encoding="utf-8"), "untouched\n")



if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import vscode_release_notes


class VSCodeReleaseNotesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp.name)
        subprocess.run(["git", "init", "-q", self.repo], check=True)
        subprocess.run(["git", "-C", self.repo, "config", "user.name", "Test"], check=True)
        subprocess.run(["git", "-C", self.repo, "config", "user.email", "test@example.com"], check=True)
        self.write_release("1.0.0", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        self.commit("initial release")
        subprocess.run(["git", "-C", self.repo, "tag", "v1.0.0"], check=True)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_release(self, version: str, changelog: str, *, production=None, dev=None) -> None:
        extension = self.repo / "extensions/vscode-lopper"
        extension.mkdir(parents=True, exist_ok=True)
        if production is None:
            production = {"tar": "^1.0.0"}
        if dev is None:
            dev = {"mocha": "^1.0.0"}
        package = {"name": "vscode-lopper", "version": version, "dependencies": production, "devDependencies": dev}
        packages = {"": {"name": "vscode-lopper", "version": version, "dependencies": production, "devDependencies": dev}}
        for name, constraint in production.items():
            packages[f"node_modules/{name}"] = {"version": constraint.lstrip("^")}
        for name, constraint in dev.items():
            packages[f"node_modules/{name}"] = {"version": constraint.lstrip("^")}
        (extension / "package.json").write_text(json.dumps(package), encoding="utf-8")
        (extension / "package-lock.json").write_text(json.dumps({"name": "vscode-lopper", "version": version, "packages": packages}), encoding="utf-8")
        (extension / "CHANGELOG.md").write_text(changelog, encoding="utf-8")
        (self.repo / "CHANGELOG.md").write_text("# Changelog\n\n## [1.0.1](x) (2026-02-02)\n\n* **vscode:** show dependency findings ([abcdef0](x))\n", encoding="utf-8")

    def commit(self, subject: str) -> str:
        subprocess.run(["git", "-C", self.repo, "add", "."], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-qm", subject], check=True)
        return subprocess.check_output(["git", "-C", self.repo, "rev-parse", "HEAD"], text=True).strip()

    def test_generates_user_visible_source_note_from_root_release_changelog(self) -> None:
        source = self.repo / "extensions/vscode-lopper/src/extension.ts"
        source.parent.mkdir(parents=True)
        source.write_text("export const findings = true;\n", encoding="utf-8")
        sha = self.commit("fix(vscode): show dependency findings")
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        root = self.repo / "CHANGELOG.md"
        root.write_text(f"# Changelog\n\n## [1.0.1](x) (2026-02-02)\n\n* **vscode:** show dependency findings ([{sha[:7]}](x))\n", encoding="utf-8")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        self.assertIn("Updated VS Code behavior: show dependency findings", (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text())

    def test_generates_note_for_user_visible_extension_manifest_change(self) -> None:
        package = self.repo / vscode_release_notes.PACKAGE_PATH
        package.write_text('{"name":"vscode-lopper","version":"1.0.0","contributes":{"commands":[{"command":"lopper.run"}]}}', encoding="utf-8")
        sha = self.commit("feat(vscode): add command")
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        root = self.repo / "CHANGELOG.md"
        root.write_text(f"# Changelog\n\n## [1.0.1](x) (2026-02-02)\n\n* **vscode:** add command ([{sha[:7]}](x))\n", encoding="utf-8")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        self.assertIn("Updated VS Code behavior: add command", (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text())

    def test_generates_note_for_marketplace_visible_extension_readme_change(self) -> None:
        readme = self.repo / vscode_release_notes.EXTENSION_DIR / "README.md"
        readme.write_text("# VS Code Lopper\n\nNew Marketplace instructions.\n", encoding="utf-8")
        sha = self.commit("docs(vscode): update Marketplace instructions")
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        root = self.repo / "CHANGELOG.md"
        root.write_text(f"# Changelog\n\n## [1.0.1](x) (2026-02-02)\n\n* **vscode:** update Marketplace instructions ([{sha[:7]}](x))\n", encoding="utf-8")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        self.assertIn("Updated VS Code behavior: update Marketplace instructions", (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text())

    def test_generates_note_for_configured_marketplace_icon_change_only(self) -> None:
        package = self.repo / vscode_release_notes.PACKAGE_PATH
        package_data = json.loads(package.read_text(encoding="utf-8"))
        package_data["icon"] = "images/lopper-icon.png"
        package.write_text(json.dumps(package_data), encoding="utf-8")
        icon = self.repo / vscode_release_notes.EXTENSION_DIR / "images/lopper-icon.png"
        icon.parent.mkdir(parents=True)
        icon.write_bytes(b"new icon")
        sha = self.commit("docs(vscode): update Marketplace icon")
        self.assertFalse(vscode_release_notes.marketplace_visible_package_document_changed(
            self.repo,
            sha,
            [(vscode_release_notes.EXTENSION_DIR / "images/not-the-icon.png").as_posix()],
        ))
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        root = self.repo / "CHANGELOG.md"
        root.write_text(f"# Changelog\n\n## [1.0.1](x) (2026-02-02)\n\n* **vscode:** update Marketplace icon ([{sha[:7]}](x))\n", encoding="utf-8")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        self.assertIn("Updated VS Code behavior: update Marketplace icon", (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text())

    def test_rejects_unsafe_configured_marketplace_icon_path(self) -> None:
        package = self.repo / vscode_release_notes.PACKAGE_PATH
        package_data = json.loads(package.read_text(encoding="utf-8"))
        package_data["icon"] = "../README.md"
        package.write_text(json.dumps(package_data), encoding="utf-8")
        sha = self.commit("test: configure unsafe Marketplace icon")
        self.assertIsNone(vscode_release_notes.configured_marketplace_icon_path(self.repo, sha))

    def test_includes_shipped_dependency_and_omits_dev_only_dependency(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n", production={"tar": "^2.0.0"}, dev={"mocha": "^2.0.0"})
        self.commit("chore(deps): refresh packages")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        output = (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text()
        self.assertIn("bundled `tar` dependency from 1.0.0 to 2.0.0", output)
        self.assertNotIn("mocha", output)

    def test_reports_production_dependency_removal(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n", production={}, dev={"tar": "^1.0.0"})
        self.commit("chore(deps): remove bundled package")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        output = (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text()
        self.assertIn("Removed the bundled `tar` dependency previously at 1.0.0", output)

    def test_reports_dependency_promoted_from_dev_to_production(self) -> None:
        old = {"packages": {"": {"devDependencies": {"tar": "^1.0.0"}}, "node_modules/tar": {"version": "1.0.0"}}}
        current = {"packages": {"": {"dependencies": {"tar": "^1.0.0"}}, "node_modules/tar": {"version": "1.0.0"}}}
        self.assertEqual(vscode_release_notes.changed_dependency_notes(old, current), ["Added the bundled `tar` dependency at 1.0.0."])

    def test_no_extension_delta_has_explicit_empty_note(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        self.commit("chore: release")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        self.assertIn("No user-visible VS Code extension changes.", (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text())

    def test_refresh_replaces_existing_entry_without_duplicates_or_stale_date(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.1 (2025-01-01)\n\n- Stale.\n\n## 1.0.1 (2025-01-02)\n\n- Duplicate.\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        self.commit("chore: release")
        vscode_release_notes.generate(self.repo, "v1.0.0", "2026-02-02")
        output = (self.repo / vscode_release_notes.CHANGELOG_PATH).read_text()
        self.assertEqual(output.count("## 1.0.1"), 1)
        self.assertIn("## 1.0.1 (2026-02-02)", output)

    def test_validation_rejects_mismatched_or_duplicate_versions(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        errors = vscode_release_notes.validate(self.repo)
        self.assertTrue(any("newest entry" in error for error in errors))
        self.assertTrue(any("exactly one" in error for error in errors))

    def test_validation_accepts_linked_changelog_headings(self) -> None:
        self.write_release("1.0.1", "# Changelog\n\n## [1.0.1](https://example.invalid) (2026-02-02)\n\n- Current release.\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
        self.assertEqual(vscode_release_notes.validate(self.repo), [])

    def test_validation_rejects_changelog_symlink_outside_repo(self) -> None:
        outside = self.repo.parent / "outside-changelog.md"
        outside.write_text("# Changelog\n", encoding="utf-8")
        changelog = self.repo / vscode_release_notes.CHANGELOG_PATH
        changelog.unlink()
        changelog.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "outside the repository"):
            vscode_release_notes.validate(self.repo)

    def test_write_rejects_extension_changelog_symlink_outside_repo(self) -> None:
        outside = self.repo.parent / "outside-changelog.md"
        outside.write_text("# Changelog\n", encoding="utf-8")
        changelog = self.repo / vscode_release_notes.CHANGELOG_PATH
        changelog.unlink()
        changelog.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "outside the repository"):
            vscode_release_notes.write_extension_changelog(self.repo, "unsafe\n")
        self.assertEqual(outside.read_text(encoding="utf-8"), "# Changelog\n")

    def test_write_targets_fixed_extension_changelog_in_temp_repo(self) -> None:
        vscode_release_notes.write_extension_changelog(self.repo, "# Changelog\n\n- Updated.\n")
        changelog = self.repo / vscode_release_notes.CHANGELOG_PATH
        self.assertEqual(changelog.read_text(encoding="utf-8"), "# Changelog\n\n- Updated.\n")

    def test_generation_rejects_non_stable_tag_before_running_git(self) -> None:
        with self.assertRaises(ValueError):
            vscode_release_notes.generate(self.repo, "v1.0.0; touch unsafe", "2026-02-02")

    def test_clean_root_note_removes_trailing_release_links_without_regex(self) -> None:
        note = vscode_release_notes.clean_root_note("* **vscode:** show findings ([#1](x)) ([abcdef0](x))")
        self.assertEqual(note, "Updated VS Code behavior: show findings")

if __name__ == "__main__":
    unittest.main()

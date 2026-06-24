#!/usr/bin/env python3

import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import read_package_version

PACKAGE_JSON_PATH = "extensions/vscode-lopper/package.json"


class ResolvePackagePathTest(unittest.TestCase):
    def setUp(self) -> None:
        self.repo_root = Path(__file__).resolve().parent.parent
        self.original_cwd = Path.cwd()

    def tearDown(self) -> None:
        os.chdir(self.original_cwd)

    def test_resolves_repo_relative_package_path(self) -> None:
        path = read_package_version.resolve_package_path(PACKAGE_JSON_PATH)
        self.assertEqual(path, self.repo_root / PACKAGE_JSON_PATH)

    def test_resolves_caller_relative_package_path_inside_repo(self) -> None:
        os.chdir(self.repo_root / "scripts")

        path = read_package_version.resolve_package_path(f"../{PACKAGE_JSON_PATH}")

        self.assertEqual(path, self.repo_root / PACKAGE_JSON_PATH)

    def test_rejects_non_package_json_basename(self) -> None:
        with self.assertRaises(ValueError):
            read_package_version.resolve_package_path("extensions/vscode-lopper/package-lock.json")

    def test_rejects_path_outside_repo(self) -> None:
        with self.assertRaises(ValueError):
            read_package_version.resolve_package_path("../package.json")


if __name__ == "__main__":
    unittest.main()

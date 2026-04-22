# Changelog

## [1.4.1](https://github.com/ben-ranford/lopper/compare/v1.4.0...v1.4.1) (2026-04-22)


### Bug Fixes

* keep skipped-name repo roots walkable ([#711](https://github.com/ben-ranford/lopper/issues/711)) ([840140a](https://github.com/ben-ranford/lopper/commit/840140a26f24d6d8d3b5b72afa3e572d44bef18d))

## 1.4.0

- Added `package`, `repo`, and `changed-packages` scope controls in VS Code, plus force-fresh refresh commands and a tighter refresh session lifecycle.
- Expanded dependency discovery with pnpm and Yarn workspace catalog support for JS/TS analysis.
- Added managed Maven dependency resolution and indexed Gradle version catalog lookup for JVM projects.
- Made runtime-assisted analysis cache-aware and hardened the PHP, Python, Ruby, and Elixir adapter pipelines for the shared `v1.4.0` release.

## 1.2.0

- Expanded package ecosystem coverage across existing adapters with Ruby gemspec support, Swift CocoaPods support, Python modern packaging support, Gradle version catalog support, and C/C++ vcpkg plus Conan support.
- Landed the supporting correctness and refactor batch that hardened release readiness across lockfile drift, reporting, runtime cancellation, registry safety, and the release workflow smoke path.

## 1.0.2

- Republished the Marketplace package at a new patch version to supersede the earlier `1.0.1` and `1.0.0` extension builds.
- Aligned the stable CLI release stream so GitHub release executables can publish at `v1.0.2` as well.

## 1.0.1

- Added adapter-aware `lopper.language` selection with `auto` as the default and `all` as the merged multi-adapter mode.
- Expanded editor diagnostics and hover support across the full set of supported Lopper adapters while keeping JS/TS codemod quick fixes.

## 1.0.0

- Initial Marketplace release with JS/TS diagnostics, hover context, codemod-backed quick fixes, and managed `lopper` binary downloads.

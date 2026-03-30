# Changelog

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

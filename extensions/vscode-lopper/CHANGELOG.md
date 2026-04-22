# Changelog

## [1.4.2](https://github.com/ben-ranford/lopper/compare/v1.4.1...v1.4.2) (2026-04-22)


### Features

* **featureflags:** add helper and activation paths ([#731](https://github.com/ben-ranford/lopper/issues/731)) ([ab030cf](https://github.com/ben-ranford/lopper/commit/ab030cf9893020d8aee322ecf50332ac7959cff4))
* **featureflags:** wire release channels and locks ([#733](https://github.com/ben-ranford/lopper/issues/733)) ([79f482e](https://github.com/ben-ranford/lopper/commit/79f482e44bc6e557826f407eac40726e1b30cbac))
* **flags:** add assisted graduation workflow ([#735](https://github.com/ben-ranford/lopper/issues/735)) ([9190e9f](https://github.com/ben-ranford/lopper/commit/9190e9fc1b8b4b034be672cc8ed80491fb6e28b3))


### Bug Fixes

* allow manual release publish retries ([#722](https://github.com/ben-ranford/lopper/issues/722)) ([c14d0d9](https://github.com/ben-ranford/lopper/commit/c14d0d9f8069efca82a087a47f7fd6f6b4b3d581))
* make release publish gh commands repo-explicit ([#719](https://github.com/ben-ranford/lopper/issues/719)) ([e9320fd](https://github.com/ben-ranford/lopper/commit/e9320fd67687f88971ac381de2a37d6afdf4bfda))
* publish draft releases by listed id ([#724](https://github.com/ben-ranford/lopper/issues/724)) ([8ada4ce](https://github.com/ben-ranford/lopper/commit/8ada4ce2b2790a0da73cab4d517813eb315f7716))


### Documentation

* **featureflags:** document stability process ([#734](https://github.com/ben-ranford/lopper/issues/734)) ([3f56e73](https://github.com/ben-ranford/lopper/commit/3f56e735d2553b06a77bed3442ecbb125bbf33ad))

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

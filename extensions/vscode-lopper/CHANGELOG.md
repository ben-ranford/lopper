# Changelog

## [1.5.0](https://github.com/ben-ranford/lopper/compare/v1.4.2...v1.5.0) (2026-04-24)


### Features

* **dart:** improve dependency source attribution behind preview flag ([#741](https://github.com/ben-ranford/lopper/issues/741)) ([e7dc83c](https://github.com/ben-ranford/lopper/commit/e7dc83ce93c58ccf606125fd6935ae53cb6d4388))
* **issue-453:** add powershell adapter behind preview flag ([#743](https://github.com/ben-ranford/lopper/issues/743)) ([236fb69](https://github.com/ben-ranford/lopper/commit/236fb69934fc6d8ec318abbbf4a1ddff73c25c62))
* **policy:** expand lockfile drift checks behind preview flag ([#740](https://github.com/ben-ranford/lopper/issues/740)) ([4746b02](https://github.com/ben-ranford/lopper/commit/4746b0227202400d538997b89783adc90c77fe87))
* refactor Go adapter and add vendored provenance preview ([#452](https://github.com/ben-ranford/lopper/issues/452) [#627](https://github.com/ben-ranford/lopper/issues/627)) ([#744](https://github.com/ben-ranford/lopper/issues/744)) ([94e36c9](https://github.com/ben-ranford/lopper/commit/94e36c914a514bd8b67467454c58d76d0ee069f3))
* **swift:** add carthage support behind preview flag ([#742](https://github.com/ben-ranford/lopper/issues/742)) ([2fa5ccb](https://github.com/ben-ranford/lopper/commit/2fa5ccb4401effab1433036eeffdf4303a58826c))


### Bug Fixes

* **ci:** harden PR base ref handling in CI shell steps ([#755](https://github.com/ben-ranford/lopper/issues/755)) ([283ad92](https://github.com/ben-ranford/lopper/commit/283ad92d6d5d75410dae96df402f1ab92e50dcdf))
* **ci:** source gosec pin from makefile ([#756](https://github.com/ben-ranford/lopper/issues/756)) ([5c99f86](https://github.com/ben-ranford/lopper/commit/5c99f866aa810decb29662836df0b906fa4b28d5))
* data-drive preview adapter gating ([#777](https://github.com/ben-ranford/lopper/issues/777)) ([306e9fc](https://github.com/ben-ranford/lopper/commit/306e9fc5b9165b02efe7299adfa34a6f27d0102a))
* dedupe policy pack error format ([#767](https://github.com/ben-ranford/lopper/issues/767)) ([08f4245](https://github.com/ben-ranford/lopper/commit/08f424536b1e4c8dae374769a556df06ac0e218b))
* **elixir:** attribute root-mismatched module imports ([#750](https://github.com/ben-ranford/lopper/issues/750)) ([b1fd562](https://github.com/ben-ranford/lopper/commit/b1fd5627c46d3d0958a1432ab7ccff761c94446a))
* enforce zero-threshold tolerance for uncertainty and baseline deltas ([#752](https://github.com/ben-ranford/lopper/issues/752)) ([2e9e6c5](https://github.com/ben-ranford/lopper/commit/2e9e6c5e56c66932cb091c32ec7035cf64558d4b))
* group cpp include resolution params ([#765](https://github.com/ben-ranford/lopper/issues/765)) ([2ae5255](https://github.com/ben-ranford/lopper/commit/2ae52558d288863ecbf79644ecf6859c7132b176))
* **lockfile-drift:** warn on missing preview lockfiles ([#776](https://github.com/ben-ranford/lopper/issues/776)) ([93acef3](https://github.com/ben-ranford/lopper/commit/93acef3ea674dd55b55cab55035e8d75ca2f5ca1))
* make inline suppression awk matching portable ([#746](https://github.com/ben-ranford/lopper/issues/746)) ([b872afb](https://github.com/ben-ranford/lopper/commit/b872afbb71f5fad44c1d578515205098b81ce4f4))
* **parser:** reduce sonar complexity ([#770](https://github.com/ben-ranford/lopper/issues/770)) ([63ca5c7](https://github.com/ben-ranford/lopper/commit/63ca5c7e3625f4961e40fa25f04da1533d3790c1))
* **powershell:** clear sonar issues ([#772](https://github.com/ben-ranford/lopper/issues/772)) ([afe22a5](https://github.com/ben-ranford/lopper/commit/afe22a57112fca2797e447da346eaf589e5d3562))
* reduce pack resolver complexity ([#766](https://github.com/ben-ranford/lopper/issues/766)) ([7c898c5](https://github.com/ben-ranford/lopper/commit/7c898c5b22e4f92031fa9a1b90d09bf5e365d4c2))
* **release:** restore darwin amd64 release binaries ([#754](https://github.com/ben-ranford/lopper/issues/754)) ([ab49986](https://github.com/ben-ranford/lopper/commit/ab49986f579907798804da79019710cc1f698f85))
* **workspace:** cover unstaged porcelain filename parsing ([#749](https://github.com/ben-ranford/lopper/issues/749)) ([e201c57](https://github.com/ben-ranford/lopper/commit/e201c5718ea9926d1b72e3e467c0b64e32e94e58))


### Code Refactoring

* **elixir:** isolate sanitizer state machine from parser ([#737](https://github.com/ben-ranford/lopper/issues/737)) ([777c8d9](https://github.com/ben-ranford/lopper/commit/777c8d9ecb6670fc6e6d513bd5bde53b72bfa3b2))
* **jvm:** split adapter seams for detection and scanning ([#739](https://github.com/ben-ranford/lopper/issues/739)) ([f66e095](https://github.com/ben-ranford/lopper/commit/f66e09589c3af992b429f19da33f55d46bd7a25b))
* reduce lockfile drift manifest complexity ([#771](https://github.com/ben-ranford/lopper/issues/771)) ([b672a22](https://github.com/ben-ranford/lopper/commit/b672a222b9e7a0490313ac0a9109972a4b1d3f19))
* **ruby:** split bundler parsing and provenance seams ([#738](https://github.com/ben-ranford/lopper/issues/738)) ([67a9554](https://github.com/ben-ranford/lopper/commit/67a95546951ab648f8f06342ff43e2d8c1b3240e))

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

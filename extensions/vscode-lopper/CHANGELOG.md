# Changelog

## [1.5.2](https://github.com/ben-ranford/lopper/compare/v1.5.1...v1.5.2) (2026-04-25)


### Bug Fixes

* **analysis:** fallback changed-packages when HEAD~1 is unavailable ([#914](https://github.com/ben-ranford/lopper/issues/914)) ([aae66d1](https://github.com/ben-ranford/lopper/commit/aae66d1d582b3738a1220c348b3dd60c3f94f411))
* **app:** bootstrap first run baseline store on save-baseline ([#913](https://github.com/ben-ranford/lopper/issues/913)) ([6c69402](https://github.com/ben-ranford/lopper/commit/6c694026559dabde83c227bc82816d4b01ac58d3))
* **cli:** prevent positional capture and preserve mapped run exit codes ([#809](https://github.com/ben-ranford/lopper/issues/809)) ([11ab3ff](https://github.com/ben-ranford/lopper/commit/11ab3ffb35ddd2d51ed954b2ec42fb7c16334195))
* escape tab and CR-prefixed CSV values ([#911](https://github.com/ben-ranford/lopper/issues/911)) ([8a4fe6f](https://github.com/ben-ranford/lopper/commit/8a4fe6f5074c104eb4d56c3b8ae4803566b4302d))
* **flags:** suppress repeated release PR graduation candidates ([#812](https://github.com/ben-ranford/lopper/issues/812)) ([7a9c032](https://github.com/ben-ranford/lopper/commit/7a9c032eddcd24f29be2c7f2e62f1589d6427787))
* gate unsafe runtime test command shapes ([#915](https://github.com/ben-ranford/lopper/issues/915)) ([2e0c76a](https://github.com/ben-ranford/lopper/commit/2e0c76a509e67091991f0b54a030c38936aca0d3))
* **gitexec:** strip loader injection env vars ([#910](https://github.com/ben-ranford/lopper/issues/910)) ([b06b338](https://github.com/ben-ranford/lopper/commit/b06b338424e13a8545392b0fe8a836910b884a60))
* **notify:** handle backticks in Slack repository and trigger text ([#806](https://github.com/ben-ranford/lopper/issues/806)) ([4d66e59](https://github.com/ben-ranford/lopper/commit/4d66e59421edf7769eda16a2b13fa09b80177ff5))
* **php:** parse inline use statements after open tag ([#807](https://github.com/ben-ranford/lopper/issues/807)) ([ff377c8](https://github.com/ben-ranford/lopper/commit/ff377c86ed8bded81d9933943d8a0f5ce684e91a))
* **release:** add checksum and action hardening for VS Code release ([8ed3909](https://github.com/ben-ranford/lopper/commit/8ed3909f1548087087d094c266fb85f9084f62bd))
* **release:** enforce conventional squash merge policy ([#811](https://github.com/ben-ranford/lopper/issues/811)) ([4a78290](https://github.com/ben-ranford/lopper/commit/4a78290ea15956a50189a9940beccce342008f46))
* **release:** harden managed-binary integrity and pin release workflow actions ([#916](https://github.com/ben-ranford/lopper/issues/916)) ([8ed3909](https://github.com/ben-ranford/lopper/commit/8ed3909f1548087087d094c266fb85f9084f62bd))
* **report:** recompute waste when used percent is non-positive ([#805](https://github.com/ben-ranford/lopper/issues/805)) ([aee46bb](https://github.com/ben-ranford/lopper/commit/aee46bb5817530e57223481a271af8e20ac94877))
* sanitize tab and CR prefixes in CSV sanitizer ([8a4fe6f](https://github.com/ben-ranford/lopper/commit/8a4fe6f5074c104eb4d56c3b8ae4803566b4302d))
* **thresholds:** bound local policy pack reads in explicit mode ([#912](https://github.com/ben-ranford/lopper/issues/912)) ([5a6a6e1](https://github.com/ben-ranford/lopper/commit/5a6a6e109712388ea757af499e5719e32bdb36ea))
* **ui:** preserve detail names and clamp prev pagination ([#803](https://github.com/ben-ranford/lopper/issues/803)) ([149dffc](https://github.com/ben-ranford/lopper/commit/149dffc435d471096406d26b8c3ec8d70c4f1c0e))
* **vscode-lopper:** reject workspace-escaping CLI diagnostics paths ([#917](https://github.com/ben-ranford/lopper/issues/917)) ([0346635](https://github.com/ben-ranford/lopper/commit/0346635f335ca18c6cba134a3cb66b7d7a29ebf6))
* **workspace:** return descriptive missing-ref lookup errors ([#808](https://github.com/ben-ranford/lopper/issues/808)) ([2cf35c5](https://github.com/ben-ranford/lopper/commit/2cf35c588202e2f9715e3ddf1e6f71229018d2f1))

## [1.5.1](https://github.com/ben-ranford/lopper/compare/v1.5.0...v1.5.1) (2026-04-25)


### Bug Fixes

* align Go build tag handling with go list ([#793](https://github.com/ben-ranford/lopper/issues/793)) ([ba1ebf9](https://github.com/ben-ranford/lopper/commit/ba1ebf999bb658ab8ba6b2fbb3227f7fba5b466c))
* attribute Dart local path dependency imports ([#792](https://github.com/ben-ranford/lopper/issues/792)) ([a149aef](https://github.com/ben-ranford/lopper/commit/a149aef3872e14253350399a560bd1f1f8128180))
* avoid .NET central package lockfile false positives ([#795](https://github.com/ben-ranford/lopper/issues/795)) ([35c6f86](https://github.com/ben-ranford/lopper/commit/35c6f8693b18c13ab134d166e2289ca5a19b942a))
* classify baseline regressions only for changed dependencies ([#796](https://github.com/ben-ranford/lopper/issues/796)) ([779a167](https://github.com/ben-ranford/lopper/commit/779a1678525da5c09255438ed7638615511f7f71))
* fix Swift parser comment and Carthage edge cases ([#794](https://github.com/ben-ranford/lopper/issues/794)) ([3942c77](https://github.com/ben-ranford/lopper/commit/3942c77d861cba5f198dec27088397b55bcaf14f))
* parse PowerShell Requires module options correctly ([#791](https://github.com/ben-ranford/lopper/issues/791)) ([a01b8d8](https://github.com/ben-ranford/lopper/commit/a01b8d88e5dbd3ac1f9834b2209add03af7a4411))
* **release:** include historical bug commits ([#801](https://github.com/ben-ranford/lopper/issues/801)) ([977495d](https://github.com/ben-ranford/lopper/commit/977495d6dc44eecbd2699fd3e5dc85ec254f493f))
* surface VS Code local binary validation errors ([#799](https://github.com/ben-ranford/lopper/issues/799)) ([c9db02c](https://github.com/ben-ranford/lopper/commit/c9db02c14a842998d16a65c818bfffc6c3fe4169))

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

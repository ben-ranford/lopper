# Changelog

Unreleased feature-flag migration guidance is maintained in the [v2 stable alias migration](docs/feature-flags.md#v2-stable-alias-migration) documentation so generated release entries remain chronological.

## [1.8.0](https://github.com/ben-ranford/lopper/compare/v1.7.0...v1.8.0) (2026-07-11)


### Features

* add preview reachability-weighted vulnerability prioritization ([11bdb30](https://github.com/ben-ranford/lopper/commit/11bdb30669767aac967a6324a82921cf2ad61e8d))
* **dashboard:** add preview remediation queue for [#1096](https://github.com/ben-ranford/lopper/issues/1096) ([e151054](https://github.com/ben-ranford/lopper/commit/e15105466f026ded20651f9583cdf936ec068ffa))
* **featureflags:** add v2 stable aliases ([84b4a33](https://github.com/ben-ranford/lopper/commit/84b4a33801c967d14e2534bfad344891e7f56154))
* **remediation:** support python safe codemod suggestions ([e6392af](https://github.com/ben-ranford/lopper/commit/e6392af2969fcc7cd5448e4589351d07b9fc20a8))
* **report:** add preview CycloneDX SBOM export ([#1106](https://github.com/ben-ranford/lopper/issues/1106)) ([5ea7cc0](https://github.com/ben-ranford/lopper/commit/5ea7cc02199ff17230110ef14ed450ded5b7d0b0))
* **report:** add runtime trace baseline diffs ([570de95](https://github.com/ben-ranford/lopper/commit/570de95b2f778dd99f86e394669d1964bc7e2d8a))
* **runtime:** capture Python imports during tests ([#1108](https://github.com/ben-ranford/lopper/issues/1108)) ([a2a9744](https://github.com/ben-ranford/lopper/commit/a2a974417d6d148133352eeebcfb4c6e0bdcda77))


### Bug Fixes

* **deps:** update dependency adm-zip to ^0.6.0 ([#1128](https://github.com/ben-ranford/lopper/issues/1128)) ([33eaa7c](https://github.com/ben-ranford/lopper/commit/33eaa7c5d2af6123f5763bbe1bce81a8e69fbd13))
* **deps:** update module github.com/pelletier/go-toml/v2 to v2.4.3 ([#1124](https://github.com/ben-ranford/lopper/issues/1124)) ([31ea825](https://github.com/ben-ranford/lopper/commit/31ea8252ef84913d0f60dfc6dc7107c7f1473af1))
* **deps:** update module golang.org/x/mod to v0.38.0 ([#1126](https://github.com/ben-ranford/lopper/issues/1126)) ([51759c1](https://github.com/ben-ranford/lopper/commit/51759c1def17c6358666abba8554d2caf2a30fba))

## [1.7.0](https://github.com/ben-ranford/lopper/compare/v1.6.1...v1.7.0) (2026-06-25)


### Features

* **dashboard:** support repoUrl config entries ([#1076](https://github.com/ben-ranford/lopper/issues/1076)) ([79d2750](https://github.com/ben-ranford/lopper/commit/79d2750620fad07ae269186cbe228e6b450148f3))
* **flags:** graduate LOP-FEAT-0009 to stable ([#1079](https://github.com/ben-ranford/lopper/issues/1079)) ([784ce5c](https://github.com/ben-ranford/lopper/commit/784ce5c832de8b4221cb3c735f37ad92089a4a8d))
* **flags:** graduate LOP-FEAT-0010 to stable ([#1084](https://github.com/ben-ranford/lopper/issues/1084)) ([e4b08eb](https://github.com/ben-ranford/lopper/commit/e4b08eb94c38d9c0c0a654e554bd6453899c9d0b))
* **flags:** graduate LOP-FEAT-0011 to stable ([#1085](https://github.com/ben-ranford/lopper/issues/1085)) ([d308263](https://github.com/ben-ranford/lopper/commit/d30826311f0debfc96556a51be44adc03c761f7e))
* **flags:** graduate LOP-FEAT-0012 to stable ([#1083](https://github.com/ben-ranford/lopper/issues/1083)) ([6026dfb](https://github.com/ben-ranford/lopper/commit/6026dfb034adb75946718cdf1ac406b065ab16cc))
* **mcp:** add explicit mutation tools ([#1078](https://github.com/ben-ranford/lopper/issues/1078)) ([e2cae53](https://github.com/ben-ranford/lopper/commit/e2cae53dc3e7aa68f997173576468599c8f8094a))
* **runtime:** add Python runtime trace annotations ([#1077](https://github.com/ben-ranford/lopper/issues/1077)) ([7f7105e](https://github.com/ben-ranford/lopper/commit/7f7105ebda5b85df5e24a8c710fdaa3ac9651876))
* **thresholds:** add built-in profile scaffolding ([#1075](https://github.com/ben-ranford/lopper/issues/1075)) ([af8322a](https://github.com/ben-ranford/lopper/commit/af8322a035e0726777687ab9346dd75b97faca2b))


### Bug Fixes

* **cli:** name snapshot path label ([ab832b9](https://github.com/ben-ranford/lopper/commit/ab832b9b5af7b879abe506480c2d3271f0ac4143))
* **dart:** simplify import scanning ([31565e5](https://github.com/ben-ranford/lopper/commit/31565e507d9a92014d89d964231d6993b5c11832))
* **deps:** update module github.com/pelletier/go-toml/v2 to v2.4.0 ([#1046](https://github.com/ben-ranford/lopper/issues/1046)) ([4262f6e](https://github.com/ben-ranford/lopper/commit/4262f6e7a58ed48964b4fb5d4ed7f7d553cd69c9))
* **dotnet:** simplify project reference parsing ([0f37123](https://github.com/ben-ranford/lopper/commit/0f37123593d5965600aa38149be57abf5f7066d8))
* **flags:** allow graduation PR metadata ([#1080](https://github.com/ben-ranford/lopper/issues/1080)) ([216b746](https://github.com/ben-ranford/lopper/commit/216b746637f4dbdf794b4cda084933debcb2e0c2))
* **flags:** derive graduation target series labels ([#1081](https://github.com/ben-ranford/lopper/issues/1081)) ([a180eb3](https://github.com/ben-ranford/lopper/commit/a180eb369e6d0631d96f5653c23ffa65d0416c15))
* **go:** inline adapter test condition ([2758ba3](https://github.com/ben-ranford/lopper/commit/2758ba3699a35b00ddf0f728f1f7750a6e10ec37))
* **go:** simplify helper path tests ([f1a3314](https://github.com/ben-ranford/lopper/commit/f1a3314f137f14063ce4f54088d731ea910dc5a3))
* **jvm:** simplify gradle build test ([5187005](https://github.com/ben-ranford/lopper/commit/518700584f3dfb83c958546e4ba650968da6793c))
* **jvm:** simplify gradle catalog test ([ab55645](https://github.com/ben-ranford/lopper/commit/ab55645f5eedda8cfdc9b7dc692e8d6838760852))
* **mcp:** make timeout cancellation test deterministic ([e94d2d9](https://github.com/ben-ranford/lopper/commit/e94d2d95befe39bc5dedffd2d015f06a28ba06a3))
* **release:** sanitize GHCR image tags ([#1073](https://github.com/ben-ranford/lopper/issues/1073)) ([6cb9a00](https://github.com/ben-ranford/lopper/commit/6cb9a00a0db88d32d92bf0ada104888fbb65d947))
* **release:** validate package version path ([9a2a7a0](https://github.com/ben-ranford/lopper/commit/9a2a7a07890d5d90816c0f5501fbc97c7e88fcbe))
* **report:** correct zero deltas and denied license summary ([#1072](https://github.com/ben-ranford/lopper/issues/1072)) ([e2e68b3](https://github.com/ben-ranford/lopper/commit/e2e68b3c3a1cebb55203997038d68d5771e7a818))
* **rust:** name manifest dependency sections ([77b63e3](https://github.com/ben-ranford/lopper/commit/77b63e3b78568603654f21374cf58a58a218700c))
* **rust:** simplify manifest scan path test ([4ef34f2](https://github.com/ben-ranford/lopper/commit/4ef34f204ee36341fa427f820384918da7e60d61))
* **rust:** simplify structured manifest tests ([2956b97](https://github.com/ben-ranford/lopper/commit/2956b97c6f07f9fe7199b2bd7d440e01992fc987))
* **sonar:** clear remaining main issues ([7f26f43](https://github.com/ben-ranford/lopper/commit/7f26f437ea72226dd22d7bc7484b5b56660fff2b))
* **testdata:** lock js esm fixture dependency ([59076a6](https://github.com/ben-ranford/lopper/commit/59076a69230772e5c2b888c4763085b0650bc6a2))
* **ui:** group detail row parameters ([5314da8](https://github.com/ben-ranford/lopper/commit/5314da84479c6c9027074e10a19f54a3bdc2efc6))
* **vscode:** avoid mutating reverse in path helper ([b11c4b9](https://github.com/ben-ranford/lopper/commit/b11c4b9232deefab044228ccbd3141aaa5082513))
* **vscode:** group codemod fetch parameters ([de4ce82](https://github.com/ben-ranford/lopper/commit/de4ce82eb85c7b0048940a15e9939bc6de3880fc))
* **vscode:** prefer at in runner tests ([256a817](https://github.com/ben-ranford/lopper/commit/256a817135385ed6140656a46b556042e3be5230))
* **vscode:** simplify managed binary cache assignment ([2fcf200](https://github.com/ben-ranford/lopper/commit/2fcf2004bb65c55d55ea9510d226a9f514df0dc9))
* **workspace:** simplify changed files tests ([eac8191](https://github.com/ben-ranford/lopper/commit/eac8191bdec3f007097eec05ad0a183ad150971c))


### Performance Improvements

* **analysis:** index changed package roots ([#1071](https://github.com/ben-ranford/lopper/issues/1071)) ([75275ab](https://github.com/ben-ranford/lopper/commit/75275ab2d2151cc6a8b216aec2d10bae0b92f523))

## [1.6.1](https://github.com/ben-ranford/lopper/compare/v1.6.0...v1.6.1) (2026-06-15)


### Bug Fixes

* **cli:** normalize flag parsing and version handling ([#1032](https://github.com/ben-ranford/lopper/issues/1032)) ([7de7fb8](https://github.com/ben-ranford/lopper/commit/7de7fb81372f88e14bf280e0cd09baa8951ab7ff))
* **lang:** handle inline go.mod require blocks ([#1033](https://github.com/ben-ranford/lopper/issues/1033)) ([ec3f466](https://github.com/ben-ranford/lopper/commit/ec3f466870dd35234eef021ece65a51bf010fcfd))
* **release:** gate skip-release on manual tag dispatches ([#1035](https://github.com/ben-ranford/lopper/issues/1035)) ([874d176](https://github.com/ben-ranford/lopper/commit/874d176e7b6c36eb38f69417a431feceeb7b6c4f))
* **release:** show only new stable defaults in published notes ([#1030](https://github.com/ben-ranford/lopper/issues/1030)) ([01184ae](https://github.com/ben-ranford/lopper/commit/01184ae60621c8efb64c282237bdf41ed9b65670))
* **ui:** make summary help rendering one-shot ([#1034](https://github.com/ben-ranford/lopper/issues/1034)) ([cf3927c](https://github.com/ben-ranford/lopper/commit/cf3927cc4a865873b989bd5af9f6b50eaefde9ca))
* **vscode:** harden workspace path and tar extraction ([#1037](https://github.com/ben-ranford/lopper/issues/1037)) ([3cefb67](https://github.com/ben-ranford/lopper/commit/3cefb678d1fb412376f690a84a6b06a726b1641b))
* **workspace:** preserve arrow substrings in changed files ([#1036](https://github.com/ben-ranford/lopper/issues/1036)) ([6e278a3](https://github.com/ben-ranford/lopper/commit/6e278a37365c66d4bf11ba5c89baad474aa16452))

## [1.6.0](https://github.com/ben-ranford/lopper/compare/v1.5.4...v1.6.0) (2026-06-12)


### Features

* **mcp:** add stdio server tools ([01d38ae](https://github.com/ben-ranford/lopper/commit/01d38aeacbf97c293c380bdbc669930cce94b79c))
* **report:** add baseline, provenance, and runtime context ([#977](https://github.com/ben-ranford/lopper/issues/977)) ([0faf079](https://github.com/ben-ranford/lopper/commit/0faf0792fee5e210c9a64f4d45b70682a53e728d))
* **report:** use non-relative impact signal ([2acb8b0](https://github.com/ben-ranford/lopper/commit/2acb8b0a84bc76d6d25f4a1f747589ef7d2c6a2d))
* **vscode:** add multi-root analysis workflows ([#978](https://github.com/ben-ranford/lopper/issues/978)) ([f72c0b5](https://github.com/ben-ranford/lopper/commit/f72c0b519f6d11f58009b9d8396a1b8622018f24))


### Bug Fixes

* **cli:** support output files outside dashboard ([d277fa3](https://github.com/ben-ranford/lopper/commit/d277fa3f80b0ccf0e6d61f754f756ec6498358aa))
* **codemod:** preserve mixed newlines when applying suggestions ([b65b276](https://github.com/ben-ranford/lopper/commit/b65b2760a02964f8f62721a51c8f5dbf4d8763e5))
* **core:** harden config, path, and output trust boundaries ([#976](https://github.com/ben-ranford/lopper/issues/976)) ([169988a](https://github.com/ben-ranford/lopper/commit/169988ac623c1367e39699fbd7dfce50a4736e7d))
* **dart:** harden Dart import parser comments ([f9b743d](https://github.com/ben-ranford/lopper/commit/f9b743dd04ba0d2ce22890b314c35ddbff5eb558))
* **dart:** honor raw string and block comment imports ([#968](https://github.com/ben-ranford/lopper/issues/968)) ([af1c2bc](https://github.com/ben-ranford/lopper/commit/af1c2bc160e80da966c2bdb81f58318f5b02bd60))
* **dashboard:** preserve repo identity for duplicates ([62a4fe3](https://github.com/ben-ranford/lopper/commit/62a4fe3559f59bd3f39afa531982ef4555f6aa17))
* **deps:** update module golang.org/x/mod to v0.37.0 ([#981](https://github.com/ben-ranford/lopper/issues/981)) ([54beddf](https://github.com/ben-ranford/lopper/commit/54beddfe53aa6aaf1dbff8e6288dfce0b10c8b06))
* **dotnet:** ignore commented imports and package refs ([9d76b72](https://github.com/ben-ranford/lopper/commit/9d76b72dad387c477ac2a15d93afec20063c9019))
* **dotnet:** ignore commented imports and package refs ([#970](https://github.com/ben-ranford/lopper/issues/970)) ([02a4123](https://github.com/ben-ranford/lopper/commit/02a41237901e1b801c8c98f45d9f5b28f3035c0f))
* **jvm:** harden JVM and Kotlin scan paths ([a8f2a6f](https://github.com/ben-ranford/lopper/commit/a8f2a6f28b5274805dc366ca8a22442296ce1f27))
* **jvm:** harden JVM and Kotlin scan paths ([#973](https://github.com/ben-ranford/lopper/issues/973)) ([a87435b](https://github.com/ben-ranford/lopper/commit/a87435b5b7ce01a2a7571a856a6861d3465c22d0))
* **lang:** harden python and elixir manifest parsing ([72adaaa](https://github.com/ben-ranford/lopper/commit/72adaaa613fe1d4572f041e97fc375e1688f2a15))
* **lang:** harden python and elixir manifest parsing ([#969](https://github.com/ben-ranford/lopper/issues/969)) ([ced1ad6](https://github.com/ben-ranford/lopper/commit/ced1ad69339ee73d76f513236af9efa1ab28d4f4))
* **lang:** parse manifests with structured parsers ([7578657](https://github.com/ben-ranford/lopper/commit/75786573b9903400e823dc3a73294a55dd3a24a5))
* **lang:** scan hoisted js and go monorepo sources ([11024bd](https://github.com/ben-ranford/lopper/commit/11024bdc37af903d03030407110d868cd76bac98))
* **mcp:** align tool schemas with accepted inputs ([9f7e598](https://github.com/ben-ranford/lopper/commit/9f7e598586f2a9d9b64b4b94bb6f6567fb6d1921))
* **policy:** clear Sonar parameter-count issues ([#975](https://github.com/ben-ranford/lopper/issues/975)) ([87506b9](https://github.com/ben-ranford/lopper/commit/87506b98c39564f03785791f03b5371a010bf79b))
* **policy:** preserve explicit clears and upstream deny state ([620634f](https://github.com/ben-ranford/lopper/commit/620634f0d9fa0a9962f93157c5fef3dbf749b738))
* **policy:** preserve explicit clears and upstream deny state ([#972](https://github.com/ben-ranford/lopper/issues/972)) ([3ca5e1c](https://github.com/ben-ranford/lopper/commit/3ca5e1cec9081c39eb462098b42715bed848a257))
* **powershell:** tighten requires directive matching ([a939edb](https://github.com/ben-ranford/lopper/commit/a939edba748b4b366e0169b159fd7d8399f206cc))
* **powershell:** tighten requires parsing ([#967](https://github.com/ben-ranford/lopper/issues/967)) ([617f5d0](https://github.com/ben-ranford/lopper/commit/617f5d0c6c20fb2ddff6a80c8c6f2b3804077928))
* **release:** harden workflow release config ([6098a6b](https://github.com/ben-ranford/lopper/commit/6098a6b83928a4bb9c723e47d45eda6079a982bb))
* **release:** promote v1.6.0 flags and decouple manual publish from tags ([#1023](https://github.com/ben-ranford/lopper/issues/1023)) ([c03864d](https://github.com/ben-ranford/lopper/commit/c03864d7e4cedb80b7be5f6d1a78b7f41b48666d))
* **rust:** harden import and workspace parsing ([31fd3ca](https://github.com/ben-ranford/lopper/commit/31fd3ca75aba7575144f5748b92a3ce1bc9ea0da))
* **rust:** harden import and workspace parsing ([#971](https://github.com/ben-ranford/lopper/issues/971)) ([a27d9ef](https://github.com/ben-ranford/lopper/commit/a27d9ef2fe59199597390b69f733535a23650a95))
* **tui:** reuse loaded summary for detail open ([58cf28d](https://github.com/ben-ranford/lopper/commit/58cf28dcb3e9504f85ed6b2d1cb5bb6cb459b2b1))
* **vscode:** avoid sync filesystem reads on save ([8454dc4](https://github.com/ben-ranford/lopper/commit/8454dc4470295a863576a5719369e2ebbf8b7604))
* **vscode:** harden lopper execution and managed installs ([338ddba](https://github.com/ben-ranford/lopper/commit/338ddba88df24508deed6af75ddeb7f821dae209))
* **vscode:** parallelize codemod refresh analysis ([012a865](https://github.com/ben-ranford/lopper/commit/012a8655d8ad5abed30bf8e7907539b79c210572))


### Code Refactoring

* **analysis:** derive report metrics before formatting ([74eec6d](https://github.com/ben-ranford/lopper/commit/74eec6ddb0ca7c57c9c35c78154bc42dc6a8da16))
* **app:** split lockfile drift seams ([cc5b617](https://github.com/ben-ranford/lopper/commit/cc5b6172912e2fbf111fbcbbc10e6c5cd4e7a6b9))
* **js:** split adapter dependency seams ([ac907e1](https://github.com/ben-ranford/lopper/commit/ac907e1a6c6ca3df35a0f5db0ea875ef4f3d50d1))
* **lang:** centralize dependency report weights ([2948bc3](https://github.com/ben-ranford/lopper/commit/2948bc34ede98ea6943d2e2bd805797f805d3311))
* **lang:** delegate resolver defaults and enable dupl ([1dc5d46](https://github.com/ben-ranford/lopper/commit/1dc5d46d798cfb1f489be18d27536deab35d5128))
* **lang:** route root signals through shared helper ([cbb55eb](https://github.com/ben-ranford/lopper/commit/cbb55ebabf729d16b18ae04ee6c13164a6d617bb))
* **lang:** standardize adapter file names ([dfeee59](https://github.com/ben-ranford/lopper/commit/dfeee59c89fe4db389079ca4c637460e8a881d26))
* **language:** return report result from adapters ([f167837](https://github.com/ben-ranford/lopper/commit/f16783723f6f38fb87b7f4f1f16f35404ebac047))
* **language:** use analysis options for adapters ([240f29d](https://github.com/ben-ranford/lopper/commit/240f29d7cc13d091f5a5079f1660cfd60277dfe3))
* **policy:** collapse optional pair helpers ([87506b9](https://github.com/ben-ranford/lopper/commit/87506b98c39564f03785791f03b5371a010bf79b))
* **report:** use non-relative impact signal ([2acb8b0](https://github.com/ben-ranford/lopper/commit/2acb8b0a84bc76d6d25f4a1f747589ef7d2c6a2d))
* **report:** use text templates for table sections ([5ec24b4](https://github.com/ben-ranford/lopper/commit/5ec24b4706fd96c463de70839ee99da614c74717))
* **safeio:** introduce filesystem interface ([556adaa](https://github.com/ben-ranford/lopper/commit/556adaa768c8b732dd459b7ff7a93019c282ee83))
* **tests:** rename coverage sweep files by behavior ([03472f3](https://github.com/ben-ranford/lopper/commit/03472f3dba6098b51b76415209425132976476a1))

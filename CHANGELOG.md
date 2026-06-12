# Changelog

## [1.6.1](https://github.com/ben-ranford/lopper/compare/v1.6.0...v1.6.1) (2026-06-12)


### Bug Fixes

* **release:** show only new stable defaults in published notes ([#1030](https://github.com/ben-ranford/lopper/issues/1030)) ([01184ae](https://github.com/ben-ranford/lopper/commit/01184ae60621c8efb64c282237bdf41ed9b65670))

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

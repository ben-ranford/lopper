# Lopper

[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ben-ranford/lopper/badge)](https://scorecard.dev/viewer/?uri=github.com/ben-ranford/lopper)
[![VS Code Marketplace](https://img.shields.io/badge/VS%20Code-Marketplace-0098ff?logo=visualstudiocode&logoColor=white)](https://marketplace.visualstudio.com/items?itemName=BenRanford.vscode-lopper)

Lopper ranks declared dependencies whose imports it can see little or no use of.

A low-usage score is evidence to inspect, not proof that removing a dependency is safe.

## Who it's for

- Someone checking a checkout before deleting or upgrading a dependency.
- A workflow that should write a report, compare a saved baseline, or upload SARIF.
- The VS Code extension (`BenRanford.vscode-lopper`) and MCP clients, which call the same analysis.

Lopper runs locally. The same binary is the CLI, the [GitHub Action](action.yml), and the process inside the GHCR image.

## Quickstart

### CLI

macOS and Linux with Homebrew:

```bash
brew tap ben-ranford/tap
brew install lopper
lopper analyse --top 20 --repo . --language all
```

The rolling formula tracks `main`:

```bash
brew install ben-ranford/tap/lopper-rolling
```

On Windows, download the asset for your architecture from the [latest release](https://github.com/ben-ranford/lopper/releases/latest).

Inspect one dependency, open the terminal UI, or write JSON:

```bash
lopper analyse lodash --repo . --language js-ts
lopper tui --repo . --language all
lopper analyse --top 20 --repo . --language all --format json > lopper.json
```

Place `--` before a dependency name or snapshot key that starts with a dash. Arguments after `--` stay positional. Commands such as `features` and `mcp`, which accept no positional arguments, reject them.

![Lopper ranking dependencies in the terminal](docs/demos/assets/quickstart-top.gif)

### GitHub Action

```yaml
- uses: ben-ranford/lopper@v1
  with:
    version: action
    repo: .
    language: all
    top: "20"
```

`version: action` follows the action ref when it can. For a reproducible job, pin the action ref and `version` to the same release tag, for example the tag on the [latest release](https://github.com/ben-ranford/lopper/releases/latest). The [CI guide](docs/ci-usage.md) covers baselines, pull-request comments, threshold gates, and SARIF uploads.

### Docker

Stable releases publish `ghcr.io/ben-ranford/lopper:latest` for `linux/amd64` and `linux/arm64`. The rolling workflow also tags `ghcr.io/ben-ranford/lopper:rolling`. The image entrypoint is the `lopper` binary and the process user is `lopper`, so a mounted tree has to be readable by that user.

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
docker run --rm -v "$PWD":/repo:ro -w /repo ghcr.io/ben-ranford/lopper:latest analyse --top 20 --repo . --language all
```

## Verifying releases

GitHub releases include `SHA256SUMS` for the archives and a keyless Cosign bundle, `SHA256SUMS.bundle`. The release workflow also publishes SLSA build provenance for those files and for the GHCR image. Replace `<tag>` with the release you downloaded.

```bash
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify "lopper_<tag>_linux_amd64.tar.gz" --repo ben-ranford/lopper
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity "https://github.com/ben-ranford/lopper/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  SHA256SUMS
gh attestation verify "oci://ghcr.io/ben-ranford/lopper:<tag>" --repo ben-ranford/lopper
```

Rolling releases use the same checksum files. Their Cosign certificate identity is `https://github.com/ben-ranford/lopper/.github/workflows/rolling.yml@refs/heads/main`.

The signed provenance keeps the workflow identity from GitHub's OIDC token and also records the release source commit that was built. When a release publishes `SHA256SUMS`, the GitHub Action verifies the archive and stops if that entry is missing or does not match. Releases published before this asset install with a warning. Set `checksum-strict: true` to make a missing checksum file fatal.

## Supported languages

These adapter IDs are the `--language` values in `internal/cli/usage.go`. `--language auto` selects the highest-confidence adapter. `--language all` merges every matching adapter.

| ID | What that adapter reads |
| --- | --- |
| `js-ts` | `package.json` (including npm `workspaces`), `pnpm-workspace.yaml`, Yarn catalogs in `.yarnrc.yml`, and `.js`, `.jsx`, `.mjs`, `.cjs`, `.ts`, `.tsx`, `.mts`, `.cts` sources |
| `python` | `pyproject.toml`, `Pipfile`, `Pipfile.lock`, `poetry.lock`, `uv.lock`, `requirements.txt`, `setup.py`, and `.py` sources |
| `cpp` | `CMakeLists.txt`, `Makefile`, `makefile`, `GNUmakefile`, `compile_commands.json`, `vcpkg.json`, `vcpkg-lock.json`, `conanfile.txt`, `conan.lock`, and C/C++ sources |
| `jvm` | `pom.xml`, `build.gradle`, `build.gradle.kts`, Gradle `*.versions.toml` catalogs, and `.java`, `.kt`, `.kts` sources |
| `kotlin-android` | `build.gradle`, `build.gradle.kts`, `settings.gradle`, `settings.gradle.kts`, `gradle.lockfile`, Gradle `*.versions.toml` catalogs, and `AndroidManifest.xml` |
| `go` | `go.mod` and `go.work`. `vendor/modules.txt` is included when `go-vendored-provenance` is enabled, which it is by default |
| `php` | `composer.json`, `composer.lock`, and PHP sources |
| `ruby` | `Gemfile`, `Gemfile.lock`, `*.gemspec`, and `.rb` sources |
| `rust` | `Cargo.toml`, including workspaces. `Cargo.lock` is a detection signal |
| `dotnet` | `*.csproj`, `*.fsproj`, `Directory.Packages.props`, and `*.sln` |
| `elixir` | `mix.exs`, `mix.lock`, and `.ex` / `.exs` sources |
| `swift` | `Package.swift`, `Package.resolved`, `Podfile`, and `Podfile.lock`. `Cartfile` and `Cartfile.resolved` are included when `swift-carthage` is enabled, which it is by default |
| `dart` | `pubspec.yaml`, `pubspec.yml`, `pubspec.lock`, and `.dart` sources |
| `powershell` | `.psd1` module manifests (`RequiredModules`), `.psm1` modules, and `.ps1` scripts |

Lockfile drift is a separate check from usage ranking. `internal/app/lockfile_drift_ruleset.go` compares manifests with lockfiles for npm (`package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`, `pnpm-lock.yaml`, `bun.lockb`), Bundler, Composer, Cargo, Go modules (`go.sum`), Pipenv, Poetry, and uv. The stable `lockfile-drift-ecosystem-expansion` flag, on by default, adds .NET `packages.lock.json`, Dart, Elixir, and SwiftPM.

## Reports and interfaces

- Output formats: `table`, `csv`, `json`, `sarif`, and `pr-comment`. `cyclonedx-json` requires `sbom-attestation-exports-preview`.
- Compare a run with a baseline file or a saved snapshot, and fail when the waste increase crosses a threshold.
- `lopper tui` opens the terminal UI. The Stave UI is `stave-tui-preview` and stays off unless that flag is enabled.
- `lopper mcp` serves the local stdio MCP server. `mcp-server` is stable and on by default.
- The GitHub Action accepts `dashboard` and `pr-review` modes. Both stay off unless `action-dashboard-mode-preview` or `dependency-surface-pr-review-preview` is enabled.

Other flags, including ones that stay off in stable builds, are listed in [feature flags](docs/feature-flags.md).

## Documentation

- [Roadmap](ROADMAP.md)
- [CI and GitHub Action](docs/ci-usage.md)
- [Repository configuration and threshold tuning](docs/threshold-tuning.md)
- [Feature flags and release channels](docs/feature-flags.md)
- [MCP server integration](docs/mcp.md)
- [Runtime trace annotations](scripts/runtime/)
- [Multi-repository dashboards](docs/dashboard.md)
- [Pull-request dependency review](docs/pr-review.md)
- [SARIF code scanning](docs/sarif-code-scanning.md)
- [Report schema](docs/report-schema.md)
- [Language adapter architecture](docs/extensibility.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Contributing](CONTRIBUTING.md)

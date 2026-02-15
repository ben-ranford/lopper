# CI and release workflow

This repository includes four GitHub Actions workflows:

- `.github/workflows/ci.yml`: runs checks on pull requests and pushes to `main`
- `.github/workflows/release.yml`: scheduled weekly (Saturday 12:00 UTC) semver release workflow that runs CI and publishes a GitHub release with:
  - Linux/Windows artifacts from Ubuntu (cross-compiled with `zig`)
  - Darwin artifact from macOS (native arch)
  - GHCR multi-arch image (`linux/amd64`, `linux/arm64`) tagged with the release tag and `latest`
- `.github/workflows/nightly.yml`: on push to `main`, publishes a nightly prerelease with source bundle assets and clearly marked nightly notes
- `.github/workflows/docker-ghcr.yml`: manual-only fallback to build/push the GHCR image on demand

Homebrew tap automation:

- The weekly release workflow updates `ben-ranford/homebrew-tap` `Formula/lopper.rb` from the new semver tag.
- The tap update step validates with `brew audit --strict --online`, `brew install --build-from-source`, and `brew test lopper` before push.
- Set repository secret `HOMEBREW_TAP_TOKEN` (with write access to `ben-ranford/homebrew-tap`) to enable automatic tap updates.

## Example pipeline

Equivalent minimal job:

```yaml
name: ci
on: [pull_request]
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.9.0
      - run: echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
      - run: make ci
      - run: make cov
```

## Make targets used by CI

- `make build`: build local executable at `bin/lopper`
- `make lint`: run `golangci-lint`
- `make format-check`: fail if `gofmt` changes are needed
- `make cov`: run tests with coverage profile and enforce minimum total coverage (default `COVERAGE_MIN=95`)
- `make ci`: `format-check + lint + security + test + build`
- `make toolchain-check`: verify required cross toolchain binaries
- `make toolchain-install`: install required OS toolchains (`go`, `zig`) on macOS/Linux
- `make setup`: bootstrap toolchain + module download + readiness checks
- `make release VERSION=<tag>`: build release archives in `dist/` (host platform by default)

Coverage artifacts:

- `.artifacts/coverage.out`: `go test` coverage profile
- `.artifacts/coverage-total.txt`: total percentage used by CI gating

On pull requests, if the coverage gate fails, CI posts/updates a PR comment with required vs. actual coverage.
The 95% minimum is intentionally enforced in both `Makefile` and CI workflow config to keep local and CI behavior aligned.

Cross-compilation uses `zig cc` for CGO targets.
Current cross-CGO release targets are Linux and Windows.
For Darwin artifacts, build on a macOS runner with native architecture.

To build multiple platforms:

```bash
make release VERSION=v0.1.0 PLATFORMS="linux/amd64 darwin/arm64 windows/amd64"
```

Install toolchain support with:

```bash
make toolchain-install
```

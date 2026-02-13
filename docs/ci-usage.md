# CI and release workflow

This repository includes two GitHub Actions workflows:

- `.github/workflows/ci.yml`: runs checks on pull requests and pushes to `main`
- `.github/workflows/release.yml`: on every commit to `main`, runs CI and publishes a GitHub release with:
  - Linux/Windows artifacts from Ubuntu (cross-compiled with `zig`)
  - Darwin artifact from macOS (native arch)
  - GHCR multi-arch image (`linux/amd64`, `linux/arm64`) tagged with the release tag and `latest`
- `.github/workflows/docker-ghcr.yml`: manual-only fallback to build/push the GHCR image on demand

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
- `make cov`: run tests with coverage profile and enforce minimum total coverage (default `COVERAGE_MIN=90`)
- `make ci`: `format-check + lint + security + test + build`
- `make toolchain-check`: verify required cross toolchain binaries
- `make toolchain-install`: install required OS toolchains (`go`, `zig`) on macOS/Linux
- `make setup`: bootstrap toolchain + local Go tooling + module download
- `make release VERSION=<tag>`: build release archives in `dist/` (host platform by default)

Coverage artifacts:

- `.artifacts/coverage.out`: `go test` coverage profile
- `.artifacts/coverage-total.txt`: total percentage used by CI gating

On pull requests, if the coverage gate fails, CI posts/updates a PR comment with required vs. actual coverage.

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

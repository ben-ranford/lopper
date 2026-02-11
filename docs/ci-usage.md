# CI and release workflow

This repository includes two GitHub Actions workflows:

- `.github/workflows/ci.yml`: runs checks on pull requests and pushes to `main`
- `.github/workflows/release.yml`: on every commit to `main`, runs CI and publishes a GitHub release with build artifacts

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
      - run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
      - run: echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
      - run: make ci
```

## Make targets used by CI

- `make build`: build local executable at `bin/surfarea`
- `make lint`: run `golangci-lint`
- `make format-check`: fail if `gofmt` changes are needed
- `make ci`: `format-check + lint + test + build`
- `make release VERSION=<tag>`: build release archives in `dist/` (host platform by default)

To build multiple platforms (requires compatible toolchains/libraries):

```bash
make release VERSION=v0.1.0 PLATFORMS="linux/amd64 darwin/arm64 windows/amd64"
```

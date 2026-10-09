# Marketplace version contract

`expectedMarketplaceVSCEVersion` in `scripts/release_workflow_config_test.go`
is an independent npm version expectation. Its custom manager selects only that
file and the complete named constant declaration. The existing `VSCE tooling`
package rule groups npm updates, workflow pins, and this expectation, including
major versions. The lockfile remains managed by Renovate's npm manager. Dependency
PRs still require human review; no post-upgrade commands are configured.

The normal `go test ./scripts` suite checks the narrow matcher and rejects
version mismatches, missing integrity, incorrect integrity prefixes, and empty
SHA-512 digests. The integration fixture additionally exercises the actual
Renovate extractor, name normalization, branch generation, npm manifest updater,
and regex replacement engine. It regenerates npm lockfiles in a temporary copy,
runs the actual Marketplace workflow contract after patch and major updates,
and requires behavioral failures after corrupting the version or integrity.
Unrelated assertions and fixture paths must remain untouched.

To reproduce with the reviewed upstream release (without adding a repository
dependency), use Node 24.11 or later in the Node 24 series:

```sh
npm install --prefix /tmp/lopper-renovate-proof --no-audit --no-fund renovate@44.145.1
node /tmp/lopper-renovate-proof/node_modules/renovate/dist/config-validator.js --strict renovate.json
node scripts/testdata/renovate/vsce-contract.mjs "$PWD" /tmp/lopper-renovate-proof/node_modules/renovate
```

The fixture needs Git, tar, Go, npm, and npm registry access. It makes no GitHub
writes and removes its temporary copy after verification. No scripts from updated
npm packages run (`--ignore-scripts`). Simulated registry targets are published
3.9.1 -> 3.9.2 (patch) and 3.9.2 -> 4.0.0 (major), so the lockfiles contain genuine
resolved packages and integrity metadata rather than fabricated lock entries.

Reviewed upstream: [regex manager](https://docs.renovatebot.com/modules/manager/regex/),
[grouping](https://docs.renovatebot.com/configuration-options/#groupname),
[configuration validation](https://docs.renovatebot.com/config-validation/), and
[44.145.1](https://github.com/renovatebot/renovate/releases/tag/44.145.1)
(commit `0dfb75020092789b7dc3f398e31259173f24b69f`). The deployed hosted bot's
version is unverified; this proof is bound to the pinned release above.

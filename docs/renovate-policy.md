# Reviewed Renovate policy

`renovate.json` keeps all settings explicit: no `extends` key is allowed anywhere,
including empty or null values. Dependency Dashboard is enabled without approval
gating. Semantic commits are enabled with the `deps` scope: runtime dependencies
and Go `require` updates use `fix`; tooling and other updates use `chore`.

The retained recommended behavior is a reviewed subset, not a copy of the whole
preset catalog. Artifact upload/download Actions are grouped for major updates
only. Existing Go toolchain and VSCE tooling groups remain later in rule order,
including the independent Marketplace test manager introduced by #1669.
`@types/node` uses Node versioning. GitHub digest updates get compare links;
Git ref/tag sources require GitHub detection, and pinDigest is excluded. Go x
packages get source comparison and pkg.go.dev links, with ordinary Go packages
retaining the original text/link fallback. The two dormant Actions replacement
rules preserve the official datasource guard; attest additionally requires >=4.
They do not assert that any current dependency needs replacement.

Root automerge and platformAutomerge are false. A catch-all review rule and
recursive raw JSON policy tests reject enabling them in any local context,
nonboolean/null settings, autoApprove, force, disabling updates, inherited
presets, and post-upgrade tasks. The tests cover all ten update/alert blocks,
manager contexts, rules and nested objects. No new dependencies or arbitrary
post-upgrade commands are needed.

This guarantee covers repository configuration only. External hosted `force`
settings can override local false values; the official engine fixture proves
that counterexample. Hosted final configuration/logs or operator controls must
be inspected before claiming bot-wide human review. Local tests do not validate
the hosted global configuration.

## Provenance and reproduction

Settings were reviewed against official Renovate 44.134.1 source at
`e58cc8fa19f22b6203767dbe233185574883c0bb` and 44.145.1 at
`0dfb75020092789b7dc3f398e31259173f24b69f`. The research bundle retains raw
source, npm integrity metadata, retrieval times, resolved snapshots, and the
exact ten selected rule indices (0, 1, 480, 529, 615, 690, 719, 720, 723, 724).
See the [official preset source](https://github.com/renovatebot/renovate/blob/e58cc8fa19f22b6203767dbe233185574883c0bb/lib/config/presets/internal/config.preset.ts)
and [configuration precedence](https://docs.renovatebot.com/config-overview/#config-precedence).

Public PR #1831 observed at 2026-10-09T04:42:22Z reported created/updated version
44.134.1. That dated observation is not a pin or proof of the next hosted run.
The research engine initially reused 44.145.1 dependencies for the 44.134.1
package. Implementation verification instead uses an isolated 44.134.1 npm
installation with its own dependencies and RE2, outside the repository.
Synthetic dependency records prove bounded engine behavior, not live registry
discovery or hosted PR creation.

With an external pinned installation and Node 24.11+ (Node 24 series):

```sh
node /tmp/renovate-proof/node_modules/renovate/dist/config-validator.js --strict --no-global renovate.json
node scripts/testdata/renovate/explicit-policy.mjs "$PWD" /tmp/renovate-proof/node_modules/renovate
go test ./scripts -run 'Test(Renovate|MarketplaceToolingPin|ReleaseWorkflowPreparesIntegrityBoundMarketplaceTooling)' -count=1
```

The policy fixture is offline and separate from the parent's
`vsce-contract.mjs`, which deliberately disables semantic commits and needs npm
registry access to regenerate real lockfiles. Neither engine proof is invoked
by the normal Go suite; no npm installation or network is added to Go tests.
Strict repository validation needs `--no-global` when supplying a filename.
The unchanged parent README documents its original validation invocation;
use the stricter invocation above for the integrated policy.

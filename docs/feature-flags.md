# Feature Flags

Lopper uses feature flags when a behavior needs to merge before it is ready to become a stable default.
Flags are registered in `internal/featureflags/features.json`, can be enabled by users, and are resolved differently for development, rolling, and release builds.

## Lifecycle States

| State | Meaning | Default behavior |
| --- | --- | --- |
| `preview` | Merged but still under validation, rollout, or API review. | Disabled in `dev` and `release` builds unless explicitly enabled. Enabled by default in `rolling` builds. |
| `stable` | Accepted as normal behavior for all supported users. | Enabled by default in every build channel. |

Merging a PR does not graduate a feature.
Graduation is a separate registry change from `preview` to `stable` after the maintainer has evidence that the behavior is ready to be a release default.

Stable releases can still ship preview features.
Those preview features stay default-off unless a release-specific lock enables them for that release.

## Feature Codes

Every registered feature has an immutable generated code such as `LOP-FEAT-0001`.
Use the helper to allocate codes instead of editing the number by hand:

```bash
make feature-flag NAME=example-preview DESCRIPTION="Short user-facing description"
```

or:

```bash
go run ./tools/featureflag add \
  --name example-preview \
  --description "Short user-facing description"
```

The helper appends a `preview` entry to `internal/featureflags/features.json`.
Codes are stable identifiers for config, release reports, and release locks:

```json
[
  {
    "code": "LOP-FEAT-0001",
    "name": "example-preview",
    "description": "Short user-facing description",
    "lifecycle": "preview"
  }
]
```

Do not renumber or recycle a feature code after it has shipped in any rolling or stable artifact.
Avoid renaming shipped features; if a name must change, keep the code and update the description.

Validate the registry and release locks before opening a PR:

```bash
go run ./tools/featureflag validate
```

PRs titled with the `feat` Conventional Commit type must add at least one new feature flag entry, and CI rejects any newly added flag whose lifecycle is not `preview`.
The PR enforcement workflow keeps a sticky report on feature PRs and on any PR that violates the preview-lifecycle rule.

## User Activation

Preview flags can be enabled or disabled by code or name.
CLI flags override config:

```bash
lopper analyse lodash \
  --repo . \
  --enable-feature LOP-FEAT-0001
```

```bash
lopper analyse lodash \
  --repo . \
  --disable-feature example-preview
```

Repo config uses a `features` section:

```yaml
features:
  enable:
    - LOP-FEAT-0001
  disable:
    - example-preview
```

The same flag cannot be enabled and disabled in one resolved run.
Unknown feature names or codes fail parsing so stale config is visible.

Use `lopper features` to inspect the resolved defaults for a channel:

```bash
lopper features --format table
lopper features --format json --channel rolling
lopper features --format json --channel release --release v1.4.2
```

## Build Channels

Build channel controls default behavior before explicit user overrides:

- `dev`: stable flags are on, preview flags are off.
- `rolling`: all registered flags are on.
- `release`: stable flags are on, preview flags are off unless the release has a lock.

Release and rolling workflows set the build channel through build metadata.
Local builds default to `dev`.

## Release Locks

Release locks live in `internal/featureflags/release_locks.json`.
They make selected preview features default-on for one release artifact without graduating the feature to `stable`.

Example:

```json
[
  {
    "release": "v1.4.2",
    "defaultOn": ["LOP-FEAT-0001"],
    "notes": {
      "LOP-FEAT-0001": "Preview shipped default-on for release validation."
    }
  }
]
```

`release` may include or omit the leading `v`.
`defaultOn` and `notes` keys may use feature codes or names, but codes are preferred in release locks because names can change.

Release locks are for release defaults only:

- They do not mark a feature stable.
- They do not affect rolling builds, because rolling builds already enable all flags.
- They do not replace user overrides. A user can still disable a locked preview feature explicitly.

Release-please prepares stable release PRs, but it does not infer feature stability.
Use the release PR as the final review point for any release lock that should make a preview feature default-on in that release.
If the feature should become a default in all later releases, graduate it with a separate registry lifecycle change.

Before merging a release lock, reviewers should confirm:

- the feature exists in `features.json`
- the feature is still `preview`
- the release tag matches the release being prepared
- the release notes explain why the preview is default-on
- `go run ./tools/featureflag validate` passes

## Release Reports

The release workflows generate a feature flag block for release notes.
Maintainers can preview the same output locally:

```bash
go run ./tools/featureflag report --channel rolling --release rolling-test
```

```bash
go run ./tools/featureflag report \
  --channel release \
  --release v1.4.2 \
  --previous-catalog .artifacts/previous-features.json
```

The report groups:

- stable flags enabled by default
- preview flags available by opt-in
- preview flags enabled by rolling or locked default-on for a release
- newly added preview flags since the previous catalog, when provided

Release-please PRs also receive an automated sticky comment with the same release-channel feature flag report plus promotion guidance:

- edit `internal/featureflags/release_locks.json` to ship a preview flag default-on for that release only
- run `graduate-feature.yml` to open a dedicated PR that changes a flag from `preview` to `stable`

## Graduation

Graduation is a deliberate registry update from `preview` to `stable`.
Do it in a PR that names the evidence, not as an automatic side effect of merging the preview implementation.

Minimum evidence for graduation:

- the feature has shipped in rolling with all flags enabled
- tests cover the stable path and the disabled/compatibility path when one remains
- docs and examples describe the stable behavior
- no open Sonar, Copilot, or review issues remain for the feature
- release notes or PR notes identify whether any release lock should be removed or kept for historical release reporting

Use the local helper when preparing a graduation change by hand:

```bash
make feature-flag-graduate FEATURE=LOP-FEAT-0001
```

or:

```bash
go run ./tools/featureflag graduate --feature example-preview
```

Maintainers can also use the assisted workflow to open a graduation PR with the required evidence already in the body:

```bash
gh workflow run graduate-feature.yml \
  -f feature=LOP-FEAT-0001 \
  -f evidence="Rolling builds have shipped this enabled, tests cover the stable path, and docs are updated." \
  -f compatibility_notes="No breaking config changes; disable with --disable-feature if rollback is needed." \
  -f release_lock_notes="Remove future locks for this feature after the PR merges."
```

The workflow changes only the registry lifecycle and opens a PR.
It does not merge the PR, remove release locks automatically, or infer stability from release-please.
Use the optional `issue` and `milestone` inputs when the graduation belongs somewhere other than the current feature flagging rollout issue.

After graduation, future release builds enable the feature by default through `lifecycle: "stable"`.
Remove release locks for future releases when they are no longer needed.

## Process

1. Open or update the feature issue with the user impact, rollout risk, and acceptance criteria.
2. Decide whether the work needs a flag. User-visible behavior changes, risky heuristics, release workflow changes, new default policies, or incomplete features should be flagged.
3. Generate the feature code with `make feature-flag`.
4. Implement the behavior so `preview` is default-off in release builds but default-on in rolling builds.
5. Add CLI/config examples or docs when users can opt in.
6. Use release locks only when a stable release should turn on a preview feature before graduation.
7. Graduate in a later PR with `make feature-flag-graduate` or the `graduate-feature.yml` workflow when the evidence is strong enough for stable defaults.
8. Clean up obsolete preview code paths, release locks, and docs after graduation.

# Roadmap

The newest stable tag is [v1.8.9](https://github.com/ben-ranford/lopper/releases/tag/v1.8.9). The open patch milestone is [v1.8.10](https://github.com/ben-ranford/lopper/milestone/38). This page is the work after that tranche, taken from open milestones and the issues assigned to them.

Snapshot of the GitHub milestone and issue lists on 2026-11-17. It does not update itself. Milestone descriptions are quoted from GitHub. An issue is listed because it is open and assigned to that milestone, not because this document adds scope.

## Now: v1.8.10

[v1.8.10](https://github.com/ben-ranford/lopper/milestone/38): "Patch tranche for TUI, CLI, VS Code, codemod, SARIF, and Markdown output correctness."

Open issues:

- [#1732](https://github.com/ben-ranford/lopper/issues/1732) Fix pinned gostyle panic on anonymous context fields
- [#1744](https://github.com/ben-ranford/lopper/issues/1744) Refactor(lang): share JVM detection traversal without weakening pinned-root bounds
- [#1759](https://github.com/ben-ranford/lopper/issues/1759) feat(gradle): define safe resource limits for dependency discovery
- [#1764](https://github.com/ben-ranford/lopper/issues/1764) Calibrate Stave PTY and signal test deadlines with runner evidence
- [#1912](https://github.com/ben-ranford/lopper/issues/1912) Trusted provider bootstrap digest mismatches on Windows checkout

## After v1.8.10

### v1.8.11

[v1.8.11](https://github.com/ben-ranford/lopper/milestone/39): "Patch tranche for JavaScript workspace, import, re-export, license, builtin, and usage analysis."

Open issues:

- [#1233](https://github.com/ben-ranford/lopper/issues/1233) [Security]: Workspace discovery can walk outside repo via symlink
- [#1255](https://github.com/ben-ranford/lopper/issues/1255) [Security]: Unbounded license file scan enables repository DoS
- [#1258](https://github.com/ben-ranford/lopper/issues/1258) [Security]: JS scan can be DoSed by large non-identifier callees
- [#1264](https://github.com/ben-ranford/lopper/issues/1264) [Security]: Re-export cycle warnings can amplify report output
- [#1266](https://github.com/ben-ranford/lopper/issues/1266) [Security]: Exponential re-export resolution can hang JS analysis
- [#1289](https://github.com/ben-ranford/lopper/issues/1289) [Bug]: Overbroad Node builtin filtering hides dependency imports
- [#1292](https://github.com/ben-ranford/lopper/issues/1292) [Bug]: Static destructuring hides named import usage
- [#1298](https://github.com/ben-ranford/lopper/issues/1298) [Bug]: Named imports used as objects are now undercounted
- [#1326](https://github.com/ben-ranford/lopper/issues/1326) Refactor(report): centralize finalization of derived report state
- [#1328](https://github.com/ben-ranford/lopper/issues/1328) Refactor(identity): centralize PURL construction and canonicalization
- [#1337](https://github.com/ben-ranford/lopper/issues/1337) Bug(dart): conditional imports and exports are rejected by the directive parser
- [#1339](https://github.com/ben-ranford/lopper/issues/1339) Bug(swift): access-level imports are ignored by the parser

### v1.8.12

[v1.8.12](https://github.com/ben-ranford/lopper/milestone/40): "Patch tranche for Rust and Swift workspace and import parsing plus cross-language import-resolution correctness."

Open issues:

- [#1221](https://github.com/ben-ranford/lopper/issues/1221) [Security]: Rust workspace glob matching can CPU-DoS scans
- [#1222](https://github.com/ben-ranford/lopper/issues/1222) [Security]: Quadratic Rust scan-root collapse can DoS CI scans
- [#1244](https://github.com/ben-ranford/lopper/issues/1244) [Security]: Unbounded CocoaPods lockfile parsing can exhaust CI
- [#1248](https://github.com/ben-ranford/lopper/issues/1248) [Security]: Quadratic Rust import parsing DoS
- [#1251](https://github.com/ben-ranford/lopper/issues/1251) [Security]: SwiftPM parser reads unbounded manifest and lockfile data
- [#1269](https://github.com/ben-ranford/lopper/issues/1269) [Security]: Rust use parser allows untrusted source DoS
- [#1291](https://github.com/ben-ranford/lopper/issues/1291) [Bug]: Naive block-comment stripping can hide valid imports
- [#1297](https://github.com/ben-ranford/lopper/issues/1297) [Bug]: Resolved JS dependency hides later missing imports
- [#1329](https://github.com/ben-ranford/lopper/issues/1329) Refactor(safeio): make bounded reads the parser-boundary default
- [#1331](https://github.com/ben-ranford/lopper/issues/1331) [Bug]: SARIF UNC paths emit malformed file URIs
- [#1336](https://github.com/ben-ranford/lopper/issues/1336) Refactor(vscode): centralize workspace containment and canonicalization helpers
- [#1342](https://github.com/ben-ranford/lopper/issues/1342) Bug(cpp): compile_commands fallback breaks quoted paths
- [#1360](https://github.com/ben-ranford/lopper/issues/1360) Bug(advisory): local loader discards trailing JSON documents

### v1.9.0

[v1.9.0](https://github.com/ben-ranford/lopper/milestone/30) has GitHub due date 2026-09-25. Its description is "Interoperable dependency identity, advisory and VEX workflows, portfolio SBOM output, and CI/dashboard expansion." The issues still open on it are suppression-scanner bugs, hook recovery, and refactors:

- [#1099](https://github.com/ben-ranford/lopper/issues/1099) Refactor(report): split schema, renderers, baselines, and scoring boundaries
- [#1102](https://github.com/ben-ranford/lopper/issues/1102) Arch(dashboard): align repo analysis execution with analyse policy controls
- [#1184](https://github.com/ben-ranford/lopper/issues/1184) Refactor(ci): centralize marked PR comment synchronization
- [#1185](https://github.com/ben-ranford/lopper/issues/1185) Refactor(github-action): share shell utility helpers across install and run scripts
- [#1186](https://github.com/ben-ranford/lopper/issues/1186) Refactor(vscode): centralize analysis option policy and CLI argument construction
- [#1187](https://github.com/ben-ranford/lopper/issues/1187) Refactor(lang): converge adapter path containment on the shared symlink-aware helper
- [#1188](https://github.com/ben-ranford/lopper/issues/1188) Refactor(safeio): centralize lexical path-containment checks
- [#1190](https://github.com/ben-ranford/lopper/issues/1190) Refactor(thresholds): centralize CLI and MCP policy provenance helpers
- [#1552](https://github.com/ben-ranford/lopper/issues/1552) Harden suppression-verify against the required-check publish race before making it a required status check
- [#1605](https://github.com/ben-ranford/lopper/issues/1605) [Bug]: Define deterministic selection for bounded source discovery
- [#1629](https://github.com/ben-ranford/lopper/issues/1629) [Bug]: Define hook update recovery under filesystem failures
- [#1636](https://github.com/ben-ranford/lopper/issues/1636) [Bug]: Restore managed hook paths after repository relocation
- [#1644](https://github.com/ben-ranford/lopper/issues/1644) [Bug]: Suppression scanners misclassify language literals and nested comments
- [#1646](https://github.com/ben-ranford/lopper/issues/1646) [Bug]: Suppression tracker exceeds maintainability thresholds
- [#1647](https://github.com/ben-ranford/lopper/issues/1647) [Bug]: PHP short-open-tag parser exceeds complexity threshold
- [#1651](https://github.com/ben-ranford/lopper/issues/1651) [Bug]: Suppression scanners misclassify shell dialect expansion boundaries
- [#1652](https://github.com/ben-ranford/lopper/issues/1652) [Bug]: Suppression scanners treat here-document payload as shell code
- [#1653](https://github.com/ben-ranford/lopper/issues/1653) [Bug]: Suppression scanners miss comments in direct function bodies
- [#1655](https://github.com/ben-ranford/lopper/issues/1655) [Bug]: Suppression scanners lose enclosing nested case state
- [#1656](https://github.com/ben-ranford/lopper/issues/1656) [Bug]: Suppression scanners miss time-prefixed case comments
- [#1657](https://github.com/ben-ranford/lopper/issues/1657) [Bug]: Suppression scanners conflate assignment parentheses across shells
- [#1659](https://github.com/ben-ranford/lopper/issues/1659) [Bug]: Suppression scanners misclassify shell conditional regex operands

Preview flags whose descriptions match the milestone text (`dependency-identity-preview`, `advisory-osv-sync-preview`, `vulnerability-exceptions-vex-preview`, `spdx-sbom-export-preview`, `dashboard-cyclonedx-portfolio-preview`, and the related dashboard and pull-request review flags) are already registered. Their lifecycle is still `preview`. See [feature flags](docs/feature-flags.md).

### v2.0.0

[v2.0.0](https://github.com/ben-ranford/lopper/milestone/26) has GitHub due date 2026-11-20. Its description is "Major-release differentiators: explainable transitive evidence, version-aware reachability, safe change planning, signed evidence, portfolio automation, and an adapter ecosystem."

Labeled `enhancement` and `target-series:2.0.x`:

- [#1142](https://github.com/ben-ranford/lopper/issues/1142) [Feature]: Prioritize vulnerabilities by installed-version applicability and reachability
- [#1143](https://github.com/ben-ranford/lopper/issues/1143) [Feature]: Build an explainable transitive dependency and reachability graph
- [#1144](https://github.com/ben-ranford/lopper/issues/1144) [Feature]: Simulate dependency removal and upgrade impact before changing code
- [#1145](https://github.com/ben-ranford/lopper/issues/1145) [Feature]: Apply evidence-backed remediation plans atomically
- [#1146](https://github.com/ben-ranford/lopper/issues/1146) [Feature]: Plan cross-repository remediation campaigns
- [#1147](https://github.com/ben-ranford/lopper/issues/1147) [Feature]: Publish signed dependency evidence bundles
- [#1148](https://github.com/ben-ranford/lopper/issues/1148) [Feature]: Expose portfolio evidence and remediation through read-only MCP tools
- [#1149](https://github.com/ben-ranford/lopper/issues/1149) [Feature]: Publish an external adapter SDK and conformance kit
- [#1362](https://github.com/ben-ranford/lopper/issues/1362) [A1.1] Add dry-run pilot detector for repo and GitHub CI context
- [#1363](https://github.com/ben-ranford/lopper/issues/1363) [A1.2] Generate reviewable observe-only pilot plan with goal-based defaults
- [#1364](https://github.com/ben-ranford/lopper/issues/1364) [A1.3] Materialize starter config and workflow diff in preview output
- [#1365](https://github.com/ben-ranford/lopper/issues/1365) [A1.4] Run read-only pilot preflight and report readiness blockers
- [#1366](https://github.com/ben-ranford/lopper/issues/1366) [A2.1] Add explicit observe-only learning window over baseline comparisons
- [#1367](https://github.com/ben-ranford/lopper/issues/1367) [A2.2] Summarize learning-window signal quality with volatility and confidence bands
- [#1368](https://github.com/ben-ranford/lopper/issues/1368) [A2.3] Recommend candidate baseline and starting threshold profile with traceable rationale
- [#1369](https://github.com/ben-ranford/lopper/issues/1369) [A2.4] Emit ready-to-gate decision artifact with explicit insufficient-evidence path
- [#1370](https://github.com/ben-ranford/lopper/issues/1370) [A3.1] Publish first-party GitLab observe-only CI template with artifact outputs
- [#1371](https://github.com/ben-ranford/lopper/issues/1371) [A3.2] Emit deterministic GitLab Code Quality findings from existing report data
- [#1372](https://github.com/ben-ranford/lopper/issues/1372) [A3.3] Add baseline-gate compatible exit behavior for GitLab pipelines
- [#1373](https://github.com/ben-ranford/lopper/issues/1373) [A3.4] Document safe GitLab fork and self-managed boundaries in the first-party template
- [#1492](https://github.com/ben-ranford/lopper/issues/1492) [Feature]: Evaluate and migrate the TUI to a new UI library for v2.0
- [#1493](https://github.com/ben-ranford/lopper/issues/1493) [Feature]: Preview a next-generation CLI experience for v2.0

Labeled `refactor` on the same milestone:

- [#840](https://github.com/ben-ranford/lopper/issues/840) Refactor(runtime): isolate capture orchestration from command/env/state subsystems
- [#841](https://github.com/ben-ranford/lopper/issues/841) Refactor(ui): separate summary session loop from command parsing and render/snapshot pipelines
- [#905](https://github.com/ben-ranford/lopper/issues/905) Arch: Implement data-driven/plugin adapter loader
- [#906](https://github.com/ben-ranford/lopper/issues/906) Arch: Standardize RiskCue severities using an enum
- [#908](https://github.com/ben-ranford/lopper/issues/908) Refactor: Upgrade table.go with advanced alignment and formatting
- [#1028](https://github.com/ben-ranford/lopper/issues/1028) Refactor(vscode-extension): split extension controller responsibilities out of extension.ts
- [#1045](https://github.com/ben-ranford/lopper/issues/1045) Refactor(cli): derive help and manpage output from shared command metadata
- [#1104](https://github.com/ben-ranford/lopper/issues/1104) Refactor(mcp): split tool registry, schemas, and execution handlers

### v3.0.0

[v3.0.0](https://github.com/ben-ranford/lopper/milestone/43): "Precision and trust major release: versioned framework evidence packs, external runtime and coverage imports, and generated-code provenance. Gated on the v2.0.0 adoption release."

The milestone's open-issue counter currently reads 0. These issues are open and assigned to it, with `enhancement` and `target-series:3.0.x`:

- [#1374](https://github.com/ben-ranford/lopper/issues/1374) [B1.1] Define versioned framework pack contract with deterministic activation and provenance
- [#1375](https://github.com/ben-ranford/lopper/issues/1375) [B1.2] Ship first validated high-frequency framework pack with selection evidence captured
- [#1376](https://github.com/ben-ranford/lopper/issues/1376) [B1.3] Add explicit enable-disable controls and unsupported-version diagnostics for framework packs
- [#1377](https://github.com/ben-ranford/lopper/issues/1377) [B1.4] Expose pack provenance and bounded performance impact in user-visible output
- [#1378](https://github.com/ben-ranford/lopper/issues/1378) [B2.1] Add shared external-evidence import contract and LCOV importer
- [#1379](https://github.com/ben-ranford/lopper/issues/1379) [B2.2] Merge imported LCOV evidence into existing runtime-static report semantics
- [#1380](https://github.com/ben-ranford/lopper/issues/1380) [B2.3] Add coverage.py JSON and JaCoCo XML importers through the shared contract
- [#1381](https://github.com/ben-ranford/lopper/issues/1381) [B2.4] Surface imported-evidence completeness quality in repo and dashboard outputs
- [#1382](https://github.com/ben-ranford/lopper/issues/1382) [B3.1] Add explicit generated-root declarations and separate generated evidence in the report model
- [#1383](https://github.com/ben-ranford/lopper/issues/1383) [B3.2] Map first validated generator family to outputs and owning dependencies without execution
- [#1384](https://github.com/ben-ranford/lopper/issues/1384) [B3.3] Distinguish stale generated output from active generated use in diagnostics and scoring
- [#1385](https://github.com/ben-ranford/lopper/issues/1385) [B3.4] Extend generated-provenance visibility to repo outputs with coordinated provenance language

### v4.0.0

[v4.0.0](https://github.com/ben-ranford/lopper/milestone/44): "Governance and auditability major release: owned expiring exceptions, risk-decision history and exports, and portfolio policy rollout simulation. Gated on the v3.0.0 trust release."

The milestone's open-issue counter currently reads 0. These issues are open and assigned to it, with `enhancement` and `target-series:4.0.x`:

- [#1386](https://github.com/ben-ranford/lopper/issues/1386) [C1.1] Generalize finding-level exception schema across stable identities
- [#1387](https://github.com/ben-ranford/lopper/issues/1387) [C1.2] Apply exceptions after evaluation across every report format
- [#1388](https://github.com/ben-ranford/lopper/issues/1388) [C1.3] Invalidate stale exceptions on expiry or underlying evidence change
- [#1389](https://github.com/ben-ranford/lopper/issues/1389) [C1.4] Surface upcoming-expiry, stale, and forbidden states with lifecycle events
- [#1390](https://github.com/ben-ranford/lopper/issues/1390) [C2.1] Capture append-only lifecycle events for findings and remediation
- [#1391](https://github.com/ben-ranford/lopper/issues/1391) [C2.2] Export repo-scoped JSON history with time filters and partial-history labels
- [#1392](https://github.com/ben-ranford/lopper/issues/1392) [C2.3] Export CSV and self-contained HTML with retention and redaction controls
- [#1393](https://github.com/ben-ranford/lopper/issues/1393) [C2.4] Aggregate local repository ledgers into portfolio history
- [#1394](https://github.com/ben-ranford/lopper/issues/1394) [C3.1] Compare proposed policy overlay across thresholds, profiles, license, advisory, and exceptions
- [#1395](https://github.com/ben-ranford/lopper/issues/1395) [C3.2] Classify cross-repo policy blast radius deterministically
- [#1396](https://github.com/ben-ranford/lopper/issues/1396) [C3.3] Record policy digests, baseline keys, confidence and owner distribution, and recommend pilot cohort
- [#1397](https://github.com/ben-ranford/lopper/issues/1397) [C3.4] Bound 100-repository performance and deterministic export

## Open issues with no milestone

- [#253](https://github.com/ben-ranford/lopper/issues/253) Dependency Dashboard (`dependencies`)
- [#1029](https://github.com/ben-ranford/lopper/issues/1029) Overflow: 2026-06-12 multi-pass defect audit tracker
- [#1491](https://github.com/ben-ranford/lopper/issues/1491) v1.8.6: make benchmark gate robust to unrelated PR heads
- [#1544](https://github.com/ben-ranford/lopper/issues/1544) Inline suppression tracker: fix cross-diff occurrence counting and stale-issue reconciliation
- [#1882](https://github.com/ben-ranford/lopper/issues/1882) [Bug]: Hook-preflight startup loop burns a core and spins forever after TERM
- [#1905](https://github.com/ben-ranford/lopper/issues/1905) [Bug]: Hook-install interrupt tests and native Windows cleanup teardown flake in CI

[Overflow](https://github.com/ben-ranford/lopper/milestone/15) is an open milestone with no open issues. Its description is "Overflow queue for issues that miss release freeze or exceed milestone capacity."

## Labels

Series labels currently on the repository are `target-series:1.3.x` through `target-series:1.9.x`, plus `target-series:2.0.x`, `target-series:3.0.x`, and `target-series:4.0.x`. Other labels used on the issues above include `enhancement`, `bug`, `security`, `refactor`, `smell`, `performance`, `documentation`, `good first issue`, and `help wanted`.

## Proposing a feature

Open a [feature request](https://github.com/ben-ranford/lopper/issues/new?template=feature_request.yml). The template asks for the problem, the proposed behavior, alternatives, impact, and acceptance criteria. It applies the `enhancement` label.

A maintainer assigns the milestone and any `target-series:*` label. Use an existing issue on this page when the proposal is already tracked.

Behavior that changes a default, a release workflow, or an adapter heuristic ships behind a feature flag first. The rules are in [feature flags](docs/feature-flags.md) and [Contributing](CONTRIBUTING.md#feature-flagging).

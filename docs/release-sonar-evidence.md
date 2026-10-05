# Release Sonar evidence

`scripts/verify-release-sonar.sh` requires a full source commit SHA. It searches
up to 100 pages of 500 main-branch analyses for that exact revision, checks its
analysis-specific quality gate to be `OK` with `ignoredConditions` explicitly
`false`, and requires the current completed main analysis to have zero unresolved
issues, zero accepted or false-positive issues, and zero hotspots in the unfiltered
inventory, including reviewed `SAFE` or `FIXED` hotspots. Automatic analysis can
finish after the release job starts. A valid main history without the exact source
revision is polled up to 20 times, 15 seconds apart, before failing. API errors and
malformed responses fail immediately. The wait grants no approval: all processing,
task-history, quality-gate, and inventory checks still run after the exact analysis
appears. A queued, failed, missing, or changing analysis then causes verification
to fail. Missing or malformed ignored-condition
evidence also fails closed.

An older source can remain eligible after main advances, but a passing historical
quality gate alone is insufficient. The script additionally requires the
`violations`, `accepted_issues`, `false_positive_issues`, and `security_hotspots`
historical metrics to each be exactly zero at the requested analysis's timestamp. This deliberately rejects
even reviewed historical hotspots; it does not infer a past clean inventory from
today's inventory. Missing or pruned history, missing metrics, API errors, and
values from another timestamp all fail closed. The exact source analysis and
latest main analysis identities are rechecked after reading evidence.

Sonar's [Web API](https://sonarcloud.io/web_api) exposes revision and timestamp
through `api/project_analyses/search`, analysis-specific status through
`api/qualitygates/project_status`, and dated metrics through
`api/measures/search_history`. Its [metric definitions](https://sonarcloud.io/api/metrics/search?ps=500)
describe `violations` as issues, `accepted_issues` as accepted issues, and
`false_positive_issues` as false-positive issues, and `security_hotspots` as security hotspots. Issue and hotspot search endpoints
provide branch-current inventories, not historical inventories keyed by analysis
ID. The historical metrics check supplements the current inventory checks.

## Failed analysis evidence

Completed analysis history does not record a failed reanalysis. The public
`ce/component` response only exposes the project's latest terminal task, which
can be an unrelated PR task. A later PR task can hide an earlier main failure.
Stable verification therefore requires authenticated `ce/activity` access using
the repository's existing `SONAR_TOKEN`. This endpoint requires project
administration permission; a missing token, insufficient permission, malformed
response, or network failure blocks publication.

The verifier finds a retained successful task matching the selected analysis ID
and reads failed/canceled report tasks without submission-time filters. A task
submitted before that successful analysis finished can still fail afterward.
Explicit PR and other-branch tasks are unrelated; remaining failed or canceled
tasks must have an execution time strictly before the selected success. Missing
execution timestamps, including cancellation before execution, remain ambiguous
and block publication. Task records do not reliably expose source revisions, so
an ambiguous main failure blocks even when it might belong to another commit.

SonarQube Cloud retains [background task history for six months](https://docs.sonarsource.com/sonarqube-cloud/managing-your-projects/background-tasks#background-task-history).
The verifier requires the successful task to remain available and be less than
179 days old. Cloud's activity API has no page-number pagination; a response at
the 1,000-failure limit is potentially truncated and fails closed. An old release
with missing/pruned task evidence requires fresh analysis rather than trusting
an incomplete history. Task history, queue state, and analysis identity are
checked around the inventory reads.

## Credential isolation and read-only proof

Callers forward only the named `SONAR_TOKEN` secret. It is available only to the
sanitized verifier step in `verify-source-sonar`, a separate read-only job that
loads its script from `github.workflow_sha`. The credential is sent only to the
Sonar task-history endpoint. The subsequent `verify-source-ci` job executes the
selected source on a separate runner with no secret references. Rolling builds
do not require Sonar credentials and cannot use stable publication identifiers.
The reusable source gate accepts only `release` or `rolling`. Unsupported or
empty channel values enter a secret-free validation step and fail before source
checkout; they cannot skip both jobs and appear to have verified a source.

After approval to use the existing secret and review of the exact workflow
revision, `release-source-ci.yml` can be manually dispatched with a full
`source_sha` and the `release` channel. It runs only the Sonar and source-CI jobs,
both with `contents: read`; it has no publishing jobs. Do not dispatch
`release.yml` or `docker-ghcr.yml` for this proof. A successful read-only run is
still required to establish that the existing token has the necessary access;
local mocked tests do not establish its permissions.

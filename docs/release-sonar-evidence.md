# Release Sonar evidence

`scripts/verify-release-sonar.sh` requires a full source commit SHA. It searches
up to 100 pages of 500 main-branch analyses for that exact revision, checks its
analysis-specific quality gate, and requires the current completed main analysis
to have zero unresolved issues and zero hotspots awaiting review. A queued,
failed, missing, or changing analysis causes verification to fail.

An older source can remain eligible after main advances, but a passing historical
quality gate alone is insufficient. The script additionally requires the
`violations`, `accepted_issues`, and `security_hotspots` historical metrics to each
be exactly zero at the requested analysis's timestamp. This deliberately rejects
even reviewed historical hotspots; it does not infer a past clean inventory from
today's inventory. Missing or pruned history, missing metrics, API errors, and
values from another timestamp all fail closed. The exact source analysis and
latest main analysis identities are rechecked after reading evidence.

Sonar's [Web API](https://sonarcloud.io/web_api) exposes revision and timestamp
through `api/project_analyses/search`, analysis-specific status through
`api/qualitygates/project_status`, and dated metrics through
`api/measures/search_history`. Its [metric definitions](https://sonarcloud.io/api/metrics/search?ps=500)
describe `violations` as issues, `accepted_issues` as accepted issues, and
`security_hotspots` as security hotspots. Issue and hotspot search endpoints
provide branch-current inventories, not historical inventories keyed by analysis
ID. The historical metrics check supplements the current inventory checks.

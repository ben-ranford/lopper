#!/usr/bin/env bash
set -euo pipefail

if [[ ! "${SOURCE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
  echo '::error::A full immutable source SHA is required.' >&2
  exit 1
fi

sonar_api() {
  curl --fail --silent --show-error --connect-timeout 10 --max-time 30 \
    "https://sonarcloud.io/api/$1"
}

latest_analysis() {
  sonar_api 'project_analyses/search?project=ben-ranford_lopper&branch=main&ps=1' \
    | jq -ce --arg sha "$SOURCE_SHA" '.analyses[0] | select(.revision == $sha and (.key | type == "string" and length > 0)) | {key, revision}'
}

completed_analysis() {
  sonar_api 'ce/component?component=ben-ranford_lopper&branch=main' \
    | jq -e --arg id "$analysis_id" '.queue == [] and .current.status == "SUCCESS" and .current.analysisId == $id' >/dev/null
}

before=$(latest_analysis)
analysis_id=$(jq -r '.key' <<< "$before")
completed_analysis
sonar_api "qualitygates/project_status?analysisId=${analysis_id}" \
  | jq -e '.projectStatus.status == "OK"' >/dev/null
sonar_api 'issues/search?componentKeys=ben-ranford_lopper&branch=main&resolved=false&ps=1' \
  | jq -e '.total == 0 and .issues == []' >/dev/null
sonar_api 'hotspots/search?projectKey=ben-ranford_lopper&branch=main&status=TO_REVIEW&ps=1' \
  | jq -e '.paging.total == 0 and .hotspots == []' >/dev/null
completed_analysis
after=$(latest_analysis)
if [[ "$before" != "$after" ]]; then
  echo '::error::Sonar analysis changed during publication verification.' >&2
  exit 1
fi
printf 'Verified zero unresolved Sonar issues and hotspots for %s (%s).\n' "$SOURCE_SHA" "$analysis_id"

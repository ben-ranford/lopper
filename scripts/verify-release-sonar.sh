#!/usr/bin/env bash
set -euo pipefail

if [[ ! "${SOURCE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
  echo '::error::A full immutable source SHA is required.' >&2
  exit 1
fi

sonar_api() {
  local endpoint="$1"
  curl --fail --silent --show-error --connect-timeout 10 --max-time 30 \
    "https://sonarcloud.io/api/${endpoint}"
}

source_analysis() {
  local page response latest_key match total
  for ((page = 1; page <= 100; page++)); do
    response=$(sonar_api "project_analyses/search?project=ben-ranford_lopper&branch=main&ps=500&p=${page}") || return 1
    if [[ "$page" == 1 ]]; then
      latest_key=$(jq -er '.analyses[0].key | select(type == "string" and length > 0)' <<< "$response") || return 1
    fi
    match=$(jq -c --arg sha "$SOURCE_SHA" --arg latest "$latest_key" \
      '[.analyses[] | select(.revision == $sha)] | first | select(. != null) | {key, revision, date, latest: $latest}' <<< "$response") || return 1
    if [[ -n "$match" ]]; then
      jq -ce 'select((.key | type == "string" and length > 0) and (.date | type == "string" and length > 0))' <<< "$match"
      return
    fi
    total=$(jq -er '.paging.total | select(type == "number" and . >= 0)' <<< "$response") || return 1
    if ((page * 500 >= total)); then
      break
    fi
  done
  echo '::error::No completed analysis found for the requested source SHA.' >&2
  return 1
}

completed_analysis() {
  sonar_api 'ce/component?component=ben-ranford_lopper&branch=main' \
    | jq -e --arg id "$latest_id" '.queue == [] and .current.status == "SUCCESS" and .current.analysisId == $id' >/dev/null
}

historical_zero_metrics() {
  local date encoded_date after_date encoded_after_date
  date=$(jq -r '.date' <<< "$before")
  encoded_date=$(jq -rn --arg date "$date" '$date | @uri')
  # search_history treats `to` as exclusive, so include the exact analysis
  # timestamp with a one-second-later bound and filter on the exact date below.
  after_date=$(jq -rn --arg date "$date" '$date | sub("\\+0000$"; "Z") | fromdateiso8601 + 1 | gmtime | strftime("%Y-%m-%dT%H:%M:%SZ")')
  encoded_after_date=$(jq -rn --arg date "$after_date" '$date | @uri')
  # The issue and hotspot inventories are branch-current. Historical metrics
  # must separately prove an older source was clean; missing history fails closed.
  sonar_api "measures/search_history?component=ben-ranford_lopper&branch=main&metrics=violations,accepted_issues,security_hotspots&from=${encoded_date}&to=${encoded_after_date}&ps=1000" \
    | jq -e --arg date "$date" '
      .measures as $measures |
      ["violations", "accepted_issues", "security_hotspots"] |
      all(.[]; . as $metric |
        [$measures[] | select(.metric == $metric) | .history[] | select(.date == $date) | .value] == ["0"])
    ' >/dev/null
}

before=$(source_analysis)
analysis_id=$(jq -r '.key' <<< "$before")
latest_id=$(jq -r '.latest' <<< "$before")
completed_analysis
sonar_api "qualitygates/project_status?analysisId=${analysis_id}" \
  | jq -e '.projectStatus.status == "OK"' >/dev/null
if [[ "$analysis_id" != "$latest_id" ]]; then
  historical_zero_metrics
fi
sonar_api 'issues/search?componentKeys=ben-ranford_lopper&branch=main&resolved=false&ps=1' \
  | jq -e '.total == 0 and .issues == []' >/dev/null
sonar_api 'hotspots/search?projectKey=ben-ranford_lopper&branch=main&status=TO_REVIEW&ps=1' \
  | jq -e '.paging.total == 0 and .hotspots == []' >/dev/null
completed_analysis
after=$(source_analysis)
if [[ "$before" != "$after" ]]; then
  echo '::error::Sonar analysis changed during publication verification.' >&2
  exit 1
fi
printf 'Verified zero unresolved Sonar issues and hotspots for %s (%s).\n' "$SOURCE_SHA" "$analysis_id"

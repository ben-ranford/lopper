#!/usr/bin/env bash
set -euo pipefail

if [[ ! "${SOURCE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
  echo '::error::A full immutable source SHA is required.' >&2
  exit 1
fi
if [[ -z "${SONAR_TOKEN:-}" ]]; then
  echo '::error::Authenticated Sonar task history is required for stable publication.' >&2
  exit 1
fi

sonar_api() {
  local endpoint="$1"
  set -- --fail --silent --show-error --connect-timeout 10 --max-time 30
  if [[ "$endpoint" == ce/activity\?* ]]; then
    set -- "$@" --user "${SONAR_TOKEN}:"
  fi
  curl "$@" "https://sonarcloud.io/api/${endpoint}"
}

source_analysis() {
  local page response latest_key match total
  for ((page = 1; page <= 100; page++)); do
    response=$(sonar_api "project_analyses/search?project=ben-ranford_lopper&branch=main&ps=500&p=${page}") || return 1
    if [[ "$page" == 1 ]]; then
      if jq -e '.analyses == [] and .paging.total == 0' <<< "$response" >/dev/null; then
        return 2
      fi
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
  # A valid history without this revision can mean automatic analysis is still
  # processing the push. This is not proof, but is eligible for bounded waiting.
  return 2
}

wait_for_source_analysis() {
  local attempt result outcome
  for ((attempt = 1; attempt <= 20; attempt++)); do
    if result=$(source_analysis); then
      printf '%s\n' "$result"
      return 0
    else
      outcome=$?
    fi
    # API errors and malformed evidence are not a scheduling delay.
    if [[ "$outcome" != 2 ]]; then
      return "$outcome"
    fi
    if ((attempt < 20)); then
      printf 'Waiting for completed Sonar analysis of %s (attempt %s/20).\n' "$SOURCE_SHA" "$attempt" >&2
      sleep 15
    fi
  done
  echo '::error::No completed analysis found for the requested source SHA after bounded waiting.' >&2
  return 1
}

verify_analysis_processing() {
  local latest_id="$1"
  # ce/component requires Browse permission, available on this public project.
  # Its queue includes pending and in-progress tasks across all branches; fail
  # closed for any task rather than mistaking completed history for an idle main.
  # A failed task has no analysis ID and leaves the old successful history intact.
  # Permit terminal PR tasks without treating them as evidence about main; any
  # other visible task must successfully match the latest completed main analysis.
  sonar_api 'ce/component?component=ben-ranford_lopper' \
    | jq -e --arg latest "$latest_id" '
      .queue == [] and (.current | type == "object") and
      (.current.status as $status |
        if (.current.pullRequest | type == "string" and length > 0) or
          (.current.branch | type == "string" and length > 0 and . != "main") then
          (["SUCCESS", "FAILED", "CANCELED"] | index($status)) != null
        else
          $status == "SUCCESS" and .current.analysisId == $latest
        end)
    ' >/dev/null
}

verify_task_history() {
  local successful failures completed_at now
  # Cloud exposes a bounded task list, not page-number pagination. Require the
  # selected successful task to remain retained, then inspect all retained
  # failures. A full response may be truncated and cannot prove absence.
  successful=$(sonar_api 'ce/activity?component=ben-ranford_lopper&type=REPORT&onlyCurrents=false&status=SUCCESS&ps=1000')
  completed_at=$(jq -er --arg analysis "$analysis_id" '
    [.tasks[] | select(.status == "SUCCESS" and .analysisId == $analysis)] |
    select(length == 1) | .[0].executedAt | select(type == "string" and length > 0)
  ' <<< "$successful")
  now=$(date +%s)
  failures=$(sonar_api 'ce/activity?component=ben-ranford_lopper&type=REPORT&onlyCurrents=false&status=FAILED,CANCELED&ps=1000')
  # Do not filter by submission time: a reanalysis can be queued before the
  # selected successful task finishes, then fail after it. Canceled tasks may
  # have no execution time; an ambiguous non-PR failure must block publication.
  # Cloud retains task history for six months. Stay within 179 days of the
  # retained successful boundary so later failures cannot have aged out.
  jq -e --arg completed "$completed_at" --argjson now "$now" '
    def timestamp: sub("\\+0000$"; "Z") | fromdateiso8601;
    ($completed | timestamp) as $boundary |
    $boundary <= $now and $boundary > ($now - 179 * 24 * 60 * 60) and
    (.tasks | type == "array" and length < 1000) and
    all(.tasks[];
      (.status == "FAILED" or .status == "CANCELED") and
      if (.pullRequest | type == "string" and length > 0) or
        (.branch | type == "string" and length > 0 and . != "main") then true
      else (.executedAt | timestamp) < $boundary end)
  ' <<< "$failures" >/dev/null
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
  sonar_api "measures/search_history?component=ben-ranford_lopper&branch=main&metrics=violations,accepted_issues,false_positive_issues,security_hotspots&from=${encoded_date}&to=${encoded_after_date}&ps=1000" \
    | jq -e --arg date "$date" '
      .measures as $measures |
      ["violations", "accepted_issues", "false_positive_issues", "security_hotspots"] |
      all(.[]; . as $metric |
        [$measures[] | select(.metric == $metric) | .history[] | select(.date == $date) | .value] == ["0"])
    ' >/dev/null
}

# Analysis history contains completed analyses and is scoped to main. The
# component compute-engine endpoint is not branch-scoped: a PR task can be its
# current task, so it cannot prove completion of this release's analysis.
# Require the exact source revision in history and a quality gate for its ID;
# re-read history after inventories to reject concurrent main-analysis changes.
before=$(wait_for_source_analysis)
analysis_id=$(jq -r '.key' <<< "$before")
latest_id=$(jq -r '.latest' <<< "$before")
verify_analysis_processing "$latest_id"
verify_task_history
sonar_api "qualitygates/project_status?analysisId=${analysis_id}" \
  | jq -e '.projectStatus.status == "OK" and .projectStatus.ignoredConditions == false' >/dev/null
if [[ "$analysis_id" != "$latest_id" ]]; then
  historical_zero_metrics
fi
sonar_api 'issues/search?componentKeys=ben-ranford_lopper&branch=main&resolved=false&ps=1' \
  | jq -e '.total == 0 and .issues == []' >/dev/null
# Accepted and false-positive dispositions are resolved issues, so the
# unresolved inventory alone cannot establish the zero-disposition policy.
sonar_api 'issues/search?componentKeys=ben-ranford_lopper&branch=main&issueStatuses=ACCEPTED,FALSE_POSITIVE&ps=1' \
  | jq -e '.total == 0 and .issues == []' >/dev/null
# Zero hotspots includes reviewed resolutions; a status filter would hide them.
sonar_api 'hotspots/search?projectKey=ben-ranford_lopper&branch=main&ps=1' \
  | jq -e '.paging.total == 0 and .hotspots == []' >/dev/null
verify_analysis_processing "$latest_id"
verify_task_history
after=$(source_analysis)
if [[ "$before" != "$after" ]]; then
  echo '::error::Sonar analysis changed during publication verification.' >&2
  exit 1
fi
printf 'Verified zero unresolved Sonar issues and hotspots for %s (%s).\n' "$SOURCE_SHA" "$analysis_id"

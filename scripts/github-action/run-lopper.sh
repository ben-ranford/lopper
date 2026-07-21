#!/usr/bin/env bash
set -euo pipefail

error() {
  echo "::error::$*" >&2
}

trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

lower() {
  local value="$1"
  printf '%s' "$value" | tr '[:upper:]' '[:lower:]'
}

is_pr_review_config() {
  local config_path repo_path config_name config_dir repo_dir
  config_path="$(trim "${1:-}")"
  repo_path="$(trim "${2:-.}")"
  config_path="${config_path#./}"
  case "$config_path" in
    .lopper.yml | .lopper.yaml | lopper.json)
      return 0
      ;;
    *) ;;
  esac
  if [[ "$config_path" != /* ]]; then
    return 1
  fi
  config_name="${config_path##*/}"
  case "$config_name" in
    .lopper.yml | .lopper.yaml | lopper.json) ;;
    *) return 1 ;;
  esac
  config_dir="$(cd -P -- "$(dirname -- "$config_path")" 2>/dev/null && pwd -P)" || return 1
  repo_dir="$(cd -P -- "$repo_path" 2>/dev/null && pwd -P)" || return 1
  [[ "$config_dir" == "$repo_dir" ]]
}

input_or_default() {
  local raw="${1:-}"
  local default_value="$2"
  local value
  value="$(trim "$raw")"
  if [[ -n "$value" ]]; then
    printf '%s' "$value"
    return
  fi
  printf '%s' "$default_value"
}

normalize_bool() {
  local name="$1"
  local raw="$2"
  local value
  value="$(lower "$(trim "$raw")")"
  case "$value" in
    true | false)
      printf '%s' "$value"
      ;;
    '')
      printf 'false'
      ;;
    *)
      error "${name} must be true or false, got '${raw}'."
      exit 2
      ;;
  esac
}

append_optional_value() {
  local flag="$1"
  local raw="$2"
  local value
  value="$(trim "$raw")"
  if [[ -n "$value" ]]; then
    args+=("$flag" "$value")
  fi
}

append_optional_bool_flag() {
  local flag="$1"
  local name="$2"
  local raw="$3"
  local value
  value="$(normalize_bool "$name" "$raw")"
  if [[ "$value" == "true" ]]; then
    args+=("$flag")
  fi
}

feature_enabled() {
  local feature="$1"
  local raw="${2:-}"
  local value
  local -a values=()
  raw="$(trim "$raw")"
  if [[ -z "$raw" ]]; then
    return 1
  fi
  IFS=',' read -r -a values <<< "$raw"
  for value in "${values[@]}"; do
    if [[ "$(lower "$(trim "$value")")" == "$(lower "$feature")" ]]; then
      return 0
    fi
  done
  return 1
}

write_output() {
  local name="$1"
  local value="$2"
  if [[ -z "${GITHUB_OUTPUT:-}" ]]; then
    return
  fi
  if [[ "$value" == *$'\n'* || "$value" == *$'\r'* ]]; then
    local delimiter="lopper_${name}_EOF"
    while [[ "$value" == *"$delimiter"* ]]; do
      delimiter="${delimiter}_x"
    done
    {
      printf '%s<<%s\n' "$name" "$delimiter"
      printf '%s\n' "$value"
      printf '%s\n' "$delimiter"
    } >> "$GITHUB_OUTPUT"
    return
  fi
  printf '%s=%s\n' "$name" "$value" >> "$GITHUB_OUTPUT"
}

write_report_path_output() {
  local output_path="$1"
  if [[ -n "$output_path" && "$output_path" != "-" ]]; then
    write_output "report-path" "$output_path"
  else
    write_output "report-path" ""
  fi
}

lopper_bin="$(trim "${LOPPER_BINARY:-lopper}")"
if [[ -z "$lopper_bin" ]]; then
  error "LOPPER_BINARY resolved to an empty command."
  exit 2
fi

repo="$(input_or_default "${INPUT_REPO:-}" ".")"
mode="$(lower "$(input_or_default "${INPUT_MODE:-}" "analyse")")"
language="$(input_or_default "${INPUT_LANGUAGE:-}" "all")"
shared_config="$(trim "${INPUT_CONFIG:-}")"

case "$mode" in
  analyse)
    format="$(input_or_default "${INPUT_FORMAT:-}" "table")"
    scope_mode="$(input_or_default "${INPUT_SCOPE_MODE:-}" "package")"
    cache_enabled="$(normalize_bool "cache" "${INPUT_CACHE:-true}")"

    args=(
      analyse
      --repo "$repo"
      --language "$language"
      --format "$format"
      --scope-mode "$scope_mode"
      "--cache=${cache_enabled}"
    )

    dependency="$(trim "${INPUT_DEPENDENCY:-}")"
    if [[ -n "$dependency" ]]; then
      if [[ "$dependency" == -* ]]; then
        error "dependency input must not start with '-'. Use dedicated action inputs for Lopper flags."
        exit 2
      fi
      args+=("$dependency")
    else
      top="$(input_or_default "${INPUT_TOP:-}" "20")"
      args+=(--top "$top")
    fi

    output_path="$(trim "${INPUT_OUTPUT:-}")"
    write_report_path_output "$output_path"
    append_optional_value --output "$output_path"
    append_optional_value --baseline "${INPUT_BASELINE:-}"
    append_optional_value --config "${INPUT_CONFIG:-}"
    append_optional_value --include "${INPUT_INCLUDE:-}"
    append_optional_value --exclude "${INPUT_EXCLUDE:-}"
    append_optional_value --runtime-profile "${INPUT_RUNTIME_PROFILE:-node-import}"
    append_optional_value --runtime-trace "${INPUT_RUNTIME_TRACE:-}"
    append_optional_value --runtime-test-command "${INPUT_RUNTIME_TEST_COMMAND:-}"
    append_optional_value --advisory-source "${INPUT_ADVISORY_SOURCE:-}"
    append_optional_value --cache-path "${INPUT_CACHE_PATH:-}"
    append_optional_bool_flag --cache-readonly cache-readonly "${INPUT_CACHE_READONLY:-false}"
    append_optional_value --threshold-fail-on-increase "${INPUT_THRESHOLD_FAIL_ON_INCREASE:-}"
    append_optional_value --threshold-low-confidence-warning "${INPUT_THRESHOLD_LOW_CONFIDENCE_WARNING:-}"
    append_optional_value --threshold-min-usage-percent "${INPUT_THRESHOLD_MIN_USAGE_PERCENT:-}"
    append_optional_value --threshold-max-uncertain-imports "${INPUT_THRESHOLD_MAX_UNCERTAIN_IMPORTS:-}"
    append_optional_value --threshold-reachable-vuln-priority "${INPUT_THRESHOLD_REACHABLE_VULNERABILITY_PRIORITY:-}"
    append_optional_value --lockfile-drift-policy "${INPUT_LOCKFILE_DRIFT_POLICY:-}"
    append_optional_value --license-deny "${INPUT_LICENSE_DENY:-}"
    append_optional_bool_flag --license-fail-on-deny license-fail-on-deny "${INPUT_LICENSE_FAIL_ON_DENY:-false}"
    append_optional_bool_flag --license-provenance-registry license-provenance-registry "${INPUT_LICENSE_PROVENANCE_REGISTRY:-false}"
    ;;
  dashboard)
    dashboard_repos="$(trim "${INPUT_DASHBOARD_REPOS:-}")"
    dashboard_config="$(trim "${INPUT_DASHBOARD_CONFIG:-}")"
    if ! feature_enabled "action-dashboard-mode-preview" "${INPUT_ENABLE_FEATURE:-}" && ! feature_enabled "LOP-FEAT-0027" "${INPUT_ENABLE_FEATURE:-}"; then
      error "mode=dashboard requires enable-feature=action-dashboard-mode-preview."
      exit 2
    fi
    if [[ -z "$dashboard_config" ]]; then
      dashboard_config="$shared_config"
    fi
    if [[ -z "$dashboard_repos" && -z "$dashboard_config" ]]; then
      error "mode=dashboard requires dashboard-repos or dashboard-config."
      exit 2
    fi

    dashboard_format="$(trim "${INPUT_DASHBOARD_FORMAT:-}")"
    top="$(input_or_default "${INPUT_TOP:-}" "20")"
    args=(dashboard)
    append_optional_value --format "$dashboard_format"
    args+=(--top "$top")
    append_optional_value --language "${INPUT_LANGUAGE:-}"

    output_path="$(trim "${INPUT_DASHBOARD_OUTPUT:-}")"
    if [[ -z "$output_path" ]]; then
      output_path="$(trim "${INPUT_OUTPUT:-}")"
    fi
    write_report_path_output "$output_path"
    append_optional_value --output "$output_path"
    append_optional_value --config "$dashboard_config"
    append_optional_value --repos "$dashboard_repos"
    ;;
  pr-review)
    if ! feature_enabled "dependency-surface-pr-review-preview" "${INPUT_ENABLE_FEATURE:-}" && ! feature_enabled "LOP-FEAT-0028" "${INPUT_ENABLE_FEATURE:-}" && ! is_pr_review_config "$shared_config" "$repo"; then
      error "mode=pr-review requires enable-feature=dependency-surface-pr-review-preview."
      exit 2
    fi

    pr_base="$(trim "${INPUT_PR_BASE:-}")"
    pr_head="$(trim "${INPUT_PR_HEAD:-}")"
    if [[ -z "$pr_base" || -z "$pr_head" ]]; then
      error "mode=pr-review requires pr-base and pr-head."
      exit 2
    fi

    pr_format="$(input_or_default "${INPUT_PR_REVIEW_FORMAT:-}" "markdown")"
    scope_mode="$(input_or_default "${INPUT_SCOPE_MODE:-}" "repo")"
    top="$(input_or_default "${INPUT_TOP:-}" "50")"
    args=(
      pr-review
      --repo "$repo"
      --base "$pr_base"
      --head "$pr_head"
      --format "$pr_format"
      --language "$language"
      --top "$top"
      --scope-mode "$scope_mode"
    )

    output_path="$(trim "${INPUT_PR_REVIEW_OUTPUT:-}")"
    if [[ -z "$output_path" ]]; then
      output_path="$(trim "${INPUT_OUTPUT:-}")"
    fi
    write_report_path_output "$output_path"
    append_optional_value --output "$output_path"
    append_optional_value --config "$shared_config"
    append_optional_value --include "${INPUT_INCLUDE:-}"
    append_optional_value --exclude "${INPUT_EXCLUDE:-}"
    append_optional_value --advisory-source "${INPUT_ADVISORY_SOURCE:-}"
    append_optional_value --threshold-low-confidence-warning "${INPUT_THRESHOLD_LOW_CONFIDENCE_WARNING:-}"
    append_optional_value --threshold-min-usage-percent "${INPUT_THRESHOLD_MIN_USAGE_PERCENT:-}"
    append_optional_value --threshold-reachable-vuln-priority "${INPUT_THRESHOLD_REACHABLE_VULNERABILITY_PRIORITY:-}"
    append_optional_value --license-deny "${INPUT_LICENSE_DENY:-}"
    append_optional_bool_flag --license-provenance-registry license-provenance-registry "${INPUT_LICENSE_PROVENANCE_REGISTRY:-false}"
    append_optional_bool_flag --fail-on-regression pr-fail-on-regression "${INPUT_PR_FAIL_ON_REGRESSION:-false}"
    append_optional_value --material-waste-bytes "${INPUT_PR_MATERIAL_WASTE_BYTES:-}"
    append_optional_value --max-rows "${INPUT_PR_MAX_ROWS:-}"
    ;;
  *)
    error "mode must be analyse, dashboard, or pr-review, got '${INPUT_MODE:-}'."
    exit 2
    ;;
esac

if [[ "$mode" != "pr-review" ]]; then
  append_optional_value --baseline-store "${INPUT_BASELINE_STORE:-}"
  append_optional_value --baseline-key "${INPUT_BASELINE_KEY:-}"
  append_optional_value --baseline-label "${INPUT_BASELINE_LABEL:-}"
  append_optional_bool_flag --save-baseline save-baseline "${INPUT_SAVE_BASELINE:-false}"
fi
append_optional_value --enable-feature "${INPUT_ENABLE_FEATURE:-}"
append_optional_value --disable-feature "${INPUT_DISABLE_FEATURE:-}"

if [[ "${LOPPER_ACTION_PRINT_COMMAND:-}" == "1" ]]; then
  printf 'lopper command:'
  printf ' %q' "$lopper_bin" "${args[@]}"
  printf '\n'
fi

"$lopper_bin" "${args[@]}"

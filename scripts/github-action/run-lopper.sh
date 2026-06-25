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
language="$(input_or_default "${INPUT_LANGUAGE:-}" "all")"
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
append_optional_value --baseline-store "${INPUT_BASELINE_STORE:-}"
append_optional_value --baseline-key "${INPUT_BASELINE_KEY:-}"
append_optional_value --baseline-label "${INPUT_BASELINE_LABEL:-}"
append_optional_bool_flag --save-baseline save-baseline "${INPUT_SAVE_BASELINE:-false}"
append_optional_value --config "${INPUT_CONFIG:-}"
append_optional_value --include "${INPUT_INCLUDE:-}"
append_optional_value --exclude "${INPUT_EXCLUDE:-}"
append_optional_value --runtime-profile "${INPUT_RUNTIME_PROFILE-node-import}"
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
append_optional_value --enable-feature "${INPUT_ENABLE_FEATURE:-}"
append_optional_value --disable-feature "${INPUT_DISABLE_FEATURE:-}"

if [[ "${LOPPER_ACTION_PRINT_COMMAND:-}" == "1" ]]; then
  printf 'lopper command:'
  printf ' %q' "$lopper_bin" "${args[@]}"
  printf '\n'
fi

"$lopper_bin" "${args[@]}"

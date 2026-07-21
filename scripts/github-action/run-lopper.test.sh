#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT_PATH="$ROOT_DIR/scripts/github-action/run-lopper.sh"
readonly PR_REVIEW_MODE_INPUT='INPUT_MODE=pr-review'
readonly PR_REVIEW_BASE_INPUT='INPUT_PR_BASE=1111111111111111111111111111111111111111'
readonly PR_REVIEW_HEAD_INPUT='INPUT_PR_HEAD=2222222222222222222222222222222222222222'
readonly PR_REVIEW_ENABLE_FEATURE_INPUT='INPUT_ENABLE_FEATURE=dependency-surface-pr-review-preview'
readonly PR_REVIEW_FEATURE_REQUIRED_ERROR='mode=pr-review requires enable-feature=dependency-surface-pr-review-preview.'
readonly DASHBOARD_MODE_INPUT='INPUT_MODE=dashboard'
readonly EMPTY_FEATURE_INPUT='INPUT_ENABLE_FEATURE='
readonly UNBOUND_VARIABLE_ERROR='unbound variable'
readonly SCOPE_MODE_ARG='--scope-mode'
readonly PACKAGE_SCOPE='package'

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  if [[ "$haystack" != *"$needle"* ]]; then
    fail "expected to find [$needle] in [$haystack]"
  fi
}

assert_not_contains() {
  local haystack="$1"
  local needle="$2"
  if [[ "$haystack" == *"$needle"* ]]; then
    fail "did not expect to find [$needle] in [$haystack]"
  fi
}

assert_line_present() {
  local file="$1"
  local needle="$2"
  if ! grep -Fx -- "$needle" "$file" > /dev/null; then
    fail "expected line [$needle] in $file"
  fi
}

assert_line_absent() {
  local file="$1"
  local needle="$2"
  if grep -Fx -- "$needle" "$file" > /dev/null; then
    fail "did not expect line [$needle] in $file"
  fi
}

assert_line_count() {
  local file="$1"
  local needle="$2"
  local expected="$3"
  local actual
  actual="$(grep -Fxc -- "$needle" "$file" || true)"
  if [[ "$actual" != "$expected" ]]; then
    fail "expected [$needle] count $expected in $file, got $actual"
  fi
}

run_case() {
  local case_dir
  case_dir="$(mktemp -d /tmp/lopper-action-shell-test.XXXXXX)"
  local capture_file="$case_dir/args.txt"
  local stderr_file="$case_dir/stderr.txt"
  local stdout_file="$case_dir/stdout.txt"
  local fake_lopper="$case_dir/lopper"
  local exit_code

  cat > "$fake_lopper" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$LOPPER_TEST_CAPTURE"
EOF
  chmod +x "$fake_lopper"

  if (
    export LOPPER_BINARY="$fake_lopper"
    export LOPPER_TEST_CAPTURE="$capture_file"
    export INPUT_REPO='.'
    export INPUT_LANGUAGE='all'
    unset INPUT_MODE
    unset INPUT_ENABLE_FEATURE
    unset INPUT_DISABLE_FEATURE
    unset INPUT_CONFIG
    unset INPUT_DASHBOARD_CONFIG
    unset INPUT_DASHBOARD_REPOS
    unset INPUT_DASHBOARD_FORMAT
    unset INPUT_DASHBOARD_OUTPUT
    unset INPUT_OUTPUT
    unset INPUT_TOP
    unset INPUT_PR_BASE
    unset INPUT_PR_HEAD
    unset INPUT_PR_REVIEW_FORMAT
    unset INPUT_PR_REVIEW_OUTPUT
    unset INPUT_INCLUDE
    unset INPUT_EXCLUDE
    unset INPUT_SCOPE_MODE
    unset INPUT_ADVISORY_SOURCE
    unset INPUT_THRESHOLD_LOW_CONFIDENCE_WARNING
    unset INPUT_THRESHOLD_MIN_USAGE_PERCENT
    unset INPUT_THRESHOLD_REACHABLE_VULNERABILITY_PRIORITY
    unset INPUT_LICENSE_DENY
    unset INPUT_LICENSE_PROVENANCE_REGISTRY
    unset INPUT_PR_FAIL_ON_REGRESSION
    unset INPUT_PR_MATERIAL_WASTE_BYTES
    unset INPUT_PR_MAX_ROWS
    for env_kv in "$@"; do
      local env_name="${env_kv%%=*}"
      local env_value="${env_kv#*=}"
      export "$env_name=$env_value"
    done
    bash "$SCRIPT_PATH"
  ) >"$stdout_file" 2>"$stderr_file"; then
    exit_code=0
  else
    exit_code=$?
  fi
  RUN_CASE_EXIT_CODE=$exit_code
  RUN_CASE_CAPTURE_FILE="$capture_file"
  RUN_CASE_STDERR_FILE="$stderr_file"
  return "$exit_code"
}

test_empty_feature_input_fails_cleanly() {
  if run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_REPOS=./api" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected empty feature input dashboard case to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "mode=dashboard requires enable-feature=action-dashboard-mode-preview."
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
}

test_dashboard_config_requires_feature_input() {
  if run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_CONFIG=lopper-org.yml" \
    "INPUT_CONFIG=" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected dashboard config without feature input to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "mode=dashboard requires enable-feature=action-dashboard-mode-preview."
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
  if [[ -s "$RUN_CASE_CAPTURE_FILE" ]]; then
    fail "expected dashboard config rejection before invoking lopper"
  fi
}

test_shared_config_does_not_enable_dashboard() {
  if run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_REPOS=" \
    "INPUT_CONFIG=lopper-org.yml" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected dashboard with only shared config to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "mode=dashboard requires enable-feature=action-dashboard-mode-preview."
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
  if [[ -s "$RUN_CASE_CAPTURE_FILE" ]]; then
    fail "expected shared config rejection before invoking lopper"
  fi
}

test_dashboard_config_does_not_enable_pr_review() {
  if run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "INPUT_DASHBOARD_CONFIG=lopper-org.yml" \
    "INPUT_CONFIG=" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected pr-review with only dashboard config to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "$PR_REVIEW_FEATURE_REQUIRED_ERROR"
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
}

test_shared_config_does_not_enable_pr_review() {
  if run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "INPUT_CONFIG=lopper-org.yml" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected pr-review with only shared config to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "$PR_REVIEW_FEATURE_REQUIRED_ERROR"
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
}

test_pr_review_repo_config_names_delegate_without_feature_input() {
  local config_path
  for config_path in .lopper.yml .lopper.yaml lopper.json ./.lopper.yml; do
    run_case \
      "$PR_REVIEW_MODE_INPUT" \
      "$PR_REVIEW_BASE_INPUT" \
      "$PR_REVIEW_HEAD_INPUT" \
      "INPUT_CONFIG=$config_path"
    if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
      fail "expected pr-review config case $config_path to succeed, got $RUN_CASE_EXIT_CODE"
    fi
    assert_line_present "$RUN_CASE_CAPTURE_FILE" "pr-review"
    assert_line_present "$RUN_CASE_CAPTURE_FILE" "--config"
    assert_line_present "$RUN_CASE_CAPTURE_FILE" "$config_path"
    assert_line_absent "$RUN_CASE_CAPTURE_FILE" "--enable-feature"
  done
}

test_pr_review_absolute_repo_config_delegates_without_feature_input() {
  local config_path="$ROOT_DIR/.lopper.yml"
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "INPUT_REPO=$ROOT_DIR" \
    "INPUT_CONFIG=$config_path"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected absolute repo config to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "--config"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "$config_path"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "--enable-feature"
}

test_pr_review_absolute_non_repo_config_requires_feature_input() {
  local config_path="$ROOT_DIR/../.lopper.yml"
  if run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "INPUT_REPO=$ROOT_DIR" \
    "INPUT_CONFIG=$config_path" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected absolute non-repo config to require feature input"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "$PR_REVIEW_FEATURE_REQUIRED_ERROR"
}

test_pr_review_symlink_escape_config_requires_feature_input() {
  local case_root
  case_root="$(mktemp -d /tmp/lopper-action-config-path.XXXXXX)"
  mkdir -p "$case_root/repo" "$case_root/outside"
  ln -s "$case_root/outside" "$case_root/repo/link"
  local config_path="$case_root/repo/link/../.lopper.yml"
  if run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "INPUT_REPO=$case_root/repo" \
    "INPUT_CONFIG=$config_path" \
    "$EMPTY_FEATURE_INPUT"; then
    fail "expected symlink-escaped config to require feature input"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "$PR_REVIEW_FEATURE_REQUIRED_ERROR"
}

test_explicit_feature_enablement_still_forwards() {
  run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_REPOS=./api,./web" \
    "INPUT_ENABLE_FEATURE=action-dashboard-mode-preview,remediation-routing-summaries-preview"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected explicit feature dashboard case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "--enable-feature"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "action-dashboard-mode-preview,remediation-routing-summaries-preview"
}

test_dashboard_config_selects_format_when_input_is_unset() {
  run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_CONFIG=format-from-config.yml" \
    "INPUT_ENABLE_FEATURE=action-dashboard-mode-preview"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected dashboard config format case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "--config"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "format-from-config.yml"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "--format"
}

test_dashboard_omits_language_when_input_is_unset() {
  run_case \
    "$DASHBOARD_MODE_INPUT" \
    "INPUT_DASHBOARD_REPOS=./api" \
    "INPUT_LANGUAGE=" \
    "INPUT_ENABLE_FEATURE=action-dashboard-mode-preview"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected dashboard default language case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "--language"
}

test_disabled_mode_keeps_controlled_error() {
  if run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT"; then
    fail "expected disabled pr-review case to fail"
  fi
  if [[ "$RUN_CASE_EXIT_CODE" != "2" ]]; then
    fail "expected exit code 2, got $RUN_CASE_EXIT_CODE"
  fi
  local stderr
  stderr="$(cat "$RUN_CASE_STDERR_FILE")"
  assert_contains "$stderr" "$PR_REVIEW_FEATURE_REQUIRED_ERROR"
  assert_not_contains "$stderr" "$UNBOUND_VARIABLE_ERROR"
}

test_pr_review_license_registry_true_forwards_once() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_LICENSE_PROVENANCE_REGISTRY=true"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review registry=true case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--license-provenance-registry" "1"
}

test_pr_review_license_registry_false_omits_flag() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_LICENSE_PROVENANCE_REGISTRY=false"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review registry=false case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "--license-provenance-registry"
}

test_pr_review_defaults_top_to_fifty_when_unset() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review top default case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "--top"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "50"
}

test_pr_review_defaults_scope_to_repo_when_unset() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review default scope case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "$SCOPE_MODE_ARG" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "repo"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "$PACKAGE_SCOPE"
}

test_pr_review_preserves_explicit_package_scope() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_SCOPE_MODE=package"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review explicit package scope case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "$SCOPE_MODE_ARG" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "$PACKAGE_SCOPE"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "repo"
}

test_pr_review_preserves_explicit_repo_scope() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_SCOPE_MODE=repo"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review explicit repo scope case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "$SCOPE_MODE_ARG" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "repo"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "$PACKAGE_SCOPE"
}

test_pr_review_keeps_explicit_top_twenty() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_TOP=20"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review explicit top case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--top" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "20"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "50"
}

test_pr_review_forwards_threshold_inputs() {
  run_case \
    "$PR_REVIEW_MODE_INPUT" \
    "$PR_REVIEW_BASE_INPUT" \
    "$PR_REVIEW_HEAD_INPUT" \
    "$PR_REVIEW_ENABLE_FEATURE_INPUT" \
    "INPUT_THRESHOLD_LOW_CONFIDENCE_WARNING=35" \
    "INPUT_THRESHOLD_MIN_USAGE_PERCENT=45" \
    "INPUT_THRESHOLD_REACHABLE_VULNERABILITY_PRIORITY=high"
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected pr-review threshold input case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--threshold-low-confidence-warning" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "35"
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--threshold-min-usage-percent" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "45"
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--threshold-reachable-vuln-priority" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "high"
}

test_analyse_defaults_top_to_twenty_when_unset() {
  run_case
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected analyse top default case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "analyse"
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "--top" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "20"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "50"
}

test_analyse_defaults_scope_to_package_when_unset() {
  run_case
  if [[ "$RUN_CASE_EXIT_CODE" != "0" ]]; then
    fail "expected analyse default scope case to succeed, got $RUN_CASE_EXIT_CODE"
  fi
  assert_line_count "$RUN_CASE_CAPTURE_FILE" "$SCOPE_MODE_ARG" "1"
  assert_line_present "$RUN_CASE_CAPTURE_FILE" "$PACKAGE_SCOPE"
  assert_line_absent "$RUN_CASE_CAPTURE_FILE" "repo"
}

test_empty_feature_input_fails_cleanly
test_dashboard_config_requires_feature_input
test_shared_config_does_not_enable_dashboard
test_dashboard_config_does_not_enable_pr_review
test_shared_config_does_not_enable_pr_review
test_pr_review_repo_config_names_delegate_without_feature_input
test_pr_review_absolute_repo_config_delegates_without_feature_input
test_pr_review_absolute_non_repo_config_requires_feature_input
test_pr_review_symlink_escape_config_requires_feature_input
test_explicit_feature_enablement_still_forwards
test_dashboard_config_selects_format_when_input_is_unset
test_dashboard_omits_language_when_input_is_unset
test_disabled_mode_keeps_controlled_error
test_pr_review_license_registry_true_forwards_once
test_pr_review_license_registry_false_omits_flag
test_pr_review_defaults_top_to_fifty_when_unset
test_pr_review_defaults_scope_to_repo_when_unset
test_pr_review_preserves_explicit_package_scope
test_pr_review_preserves_explicit_repo_scope
test_pr_review_keeps_explicit_top_twenty
test_pr_review_forwards_threshold_inputs
test_analyse_defaults_top_to_twenty_when_unset
test_analyse_defaults_scope_to_package_when_unset

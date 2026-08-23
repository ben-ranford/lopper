#!/bin/sh
set -eu

bench_gate_script="${1:-./scripts/bench-gate.sh}"
base_ref="${MEMORY_BENCH_BASE:-}"
event_name="${GITHUB_EVENT_NAME:-${GH_EVENT_NAME:-}}"
github_ref="${GITHUB_REF:-}"

is_pr_merge_checkout=false
case "$github_ref" in
refs/pull/*/merge)
	if [ "$event_name" = "pull_request" ]; then
		is_pr_merge_checkout=true
	fi
	;;
esac

if [ "$is_pr_merge_checkout" = true ] && [ -n "$base_ref" ] && git rev-parse --verify -q --end-of-options "HEAD^2^{commit}" >/dev/null; then
	requested_base_commit="$(git rev-parse --verify -q --end-of-options "$base_ref^{commit}" || true)"
	checkout_base_commit="$(git rev-parse --verify --end-of-options "HEAD^1^{commit}")"
	if [ -n "$requested_base_commit" ] && [ "$checkout_base_commit" != "$requested_base_commit" ] && git merge-base --is-ancestor "$requested_base_commit" "$checkout_base_commit" 2>/dev/null; then
		echo "Memory benchmark base ref '$base_ref' resolved to stale PR event base $requested_base_commit; using checked-out PR merge base $checkout_base_commit."
		export MEMORY_BENCH_BASE="$checkout_base_commit"
	fi
fi

exec "$bench_gate_script"

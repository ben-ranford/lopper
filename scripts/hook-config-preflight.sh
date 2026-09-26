#!/bin/sh
# Shared bounded Git reader for hook configuration inspection.
preflight_runner_pid=
preflight_watchdog_pid=
preflight_state_dir=
preflight_output_file=

cleanup_preflight_git() {
	if [ -n "$preflight_watchdog_pid" ]; then
		kill "$preflight_watchdog_pid" 2>/dev/null || :
		wait "$preflight_watchdog_pid" 2>/dev/null || :
		preflight_watchdog_pid=
	fi
	if [ -n "$preflight_runner_pid" ]; then
		kill -TERM "$preflight_runner_pid" 2>/dev/null || :
		wait "$preflight_runner_pid" 2>/dev/null || :
		preflight_runner_pid=
	fi
	rm -rf "$preflight_state_dir"
	preflight_state_dir=
	rm -f "$preflight_output_file"
	preflight_output_file=
	return 0
}

run_preflight_git() {
	preflight_state_dir=$(mktemp -d "${TMPDIR:-/tmp}/lopper-hooks-preflight.XXXXXX") || return 1
	mkdir "$preflight_state_dir/active" || {
		rmdir "$preflight_state_dir"
		preflight_state_dir=
		return 1
	}
	(
		git_pid=
		hold_pid=
		trap '
			trap - EXIT HUP INT TERM
			if [ -n "$git_pid" ]; then
				kill -TERM "$git_pid" 2>/dev/null || :
				wait "$git_pid" 2>/dev/null || :
			fi
			if [ -n "$hold_pid" ]; then
				kill "$hold_pid" 2>/dev/null || :
				wait "$hold_pid" 2>/dev/null || :
			fi
			exit 124
		' HUP INT TERM
		"$@" & git_pid=$!
		status=0
		wait "$git_pid" || status=$?
		git_pid=
		if rmdir "$preflight_state_dir/active" 2>/dev/null; then
			exit "$status"
		fi
		while :; do
			sleep 1 & hold_pid=$!
			wait "$hold_pid" || :
			hold_pid=
		done
	) & preflight_runner_pid=$!
	(
		sleeper_pid=
		trap '
			trap - EXIT HUP INT TERM
			if [ -n "$sleeper_pid" ]; then
				kill "$sleeper_pid" 2>/dev/null || :
				wait "$sleeper_pid" 2>/dev/null || :
			fi
			exit 0
		' HUP INT TERM
		sleep 10 & sleeper_pid=$!
		wait "$sleeper_pid" || exit 0
		sleeper_pid=
		if rmdir "$preflight_state_dir/active" 2>/dev/null; then
			printf x >"$preflight_state_dir/expired"
			kill -TERM "$preflight_runner_pid" 2>/dev/null || :
		fi
	) </dev/null >/dev/null 2>&1 & preflight_watchdog_pid=$!
	status=0
	wait "$preflight_runner_pid" || status=$?
	preflight_runner_pid=
	kill "$preflight_watchdog_pid" 2>/dev/null || :
	wait "$preflight_watchdog_pid" 2>/dev/null || :
	preflight_watchdog_pid=
	if [ -f "$preflight_state_dir/expired" ]; then
		rm -rf "$preflight_state_dir"
		preflight_state_dir=
		echo "Timed out while reading Git preflight configuration" >&2
		return 124
	fi
	rmdir "$preflight_state_dir" 2>/dev/null || rm -rf "$preflight_state_dir"
	preflight_state_dir=
	return "$status"
}

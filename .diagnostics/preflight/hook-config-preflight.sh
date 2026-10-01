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
	# A separate Bash job owns its process group even when a reader exits from
	# a TERM trap. Privileged mode prevents startup hooks or imported functions
	# from changing the supervisor; reader arguments stay positional.
	bash --noprofile --norc -p -c '
state_dir=$1
shift
interrupted=
supervisor_pid=$$
trap "interrupted=1" HUP INT TERM
# Capture only Bash job-launch diagnostics; the anchor restores reader stderr
# before its body can run. No reader or descendant starts before authorization.
exec 3>&2
{
set -m
(
	exec 2>&3 3>&-
	set +m
	# Forward direct anchor interruption to the supervisor as cancellation.
	# Catching signals also preserves normal signal handling in the reader.
	reader_interrupted=
	trap "reader_interrupted=1; kill -TERM \"\$supervisor_pid\" 2>/dev/null || :" HUP INT TERM
	printf x >"$state_dir/ready"
	# Builtins keep failed group setup from leaving any pre-reader descendants.
	# The existing watchdog bounds this brief startup authorization wait.
	while [ ! -f "$state_dir/start" ] || [ -n "$reader_interrupted" ]; do :; done
	reader_status=124
	if [ -z "$reader_interrupted" ]; then
		"$@" </dev/null & reader_pid=$!
		reader_status=0
		wait "$reader_pid" || reader_status=$?
	fi
	# Keep the group leader alive, without forking during the final group kill.
	(trap "" HUP INT TERM; exec sleep 60) & hold_pid=$!
	if [ -z "$reader_interrupted" ] && rmdir "$state_dir/active" 2>/dev/null; then
		printf "%s\n" "$reader_status" >"$state_dir/pending"
		mv "$state_dir/pending" "$state_dir/result"
	fi
	while kill -0 "$hold_pid" 2>/dev/null; do wait "$hold_pid" || :; done
) & reader_group=$!
set +m
} 2>"$state_dir/launch-error"
exec 3>&-
while [ ! -f "$state_dir/ready" ] && [ -z "$interrupted" ]; do
	[ "$(jobs -pr)" = "$reader_group" ] || {
		cat "$state_dir/launch-error" >&2
		exit 1
	}
	sleep 0.01
done
[ "$(jobs -pr)" = "$reader_group" ] || {
	cat "$state_dir/launch-error" >&2
	exit 1
}
# A live owned child cannot reuse an existing group ID. Before authorizing the
# reader, require the kernel to confirm a group with that exact child PID.
if ! kill -0 -- "-$reader_group" 2>/dev/null; then
	# The unapproved anchor has run builtins only, so it has no descendants.
	{
		kill -KILL "$reader_group" || :
		wait "$reader_group" || :
	} 2>/dev/null
	cat "$state_dir/launch-error" >&2
	printf "%s\n" "Could not establish Git preflight process group" >&2
	exit 1
fi
startup_failed=
[ ! -f "$state_dir/expired" ] || interrupted=1
if [ -n "$interrupted" ]; then
	cat "$state_dir/launch-error" >&2
fi
if [ -z "$interrupted" ] && [ -s "$state_dir/launch-error" ]; then
	# Bash calls setpgid in both parent and child. Accept this one child
	# diagnostic only after the live anchored group was proven above.
	printf "%s\n" "--: child setpgid ($reader_group to $reader_group): Operation not permitted" >"$state_dir/expected-launch-error"
	cmp -s "$state_dir/launch-error" "$state_dir/expected-launch-error" || startup_failed=1
	if [ -n "$startup_failed" ]; then
		cat "$state_dir/launch-error" >&2
	fi
fi
if [ -z "$interrupted" ] && [ -z "$startup_failed" ] && [ ! -f "$state_dir/expired" ]; then
	printf x >"$state_dir/start"
fi
# Polling closes the signal-before-wait race and bounds interrupted waits.
# Signal only the still-running anchored job owned by this supervisor.
while [ ! -f "$state_dir/result" ] && [ -z "$interrupted" ] && [ -z "$startup_failed" ]; do
	[ "$(jobs -pr)" = "$reader_group" ] || exit 1
	sleep 0.01
done
trap ":" HUP INT TERM
[ "$(jobs -pr)" = "$reader_group" ] || exit 1
kill -0 -- "-$reader_group" 2>/dev/null || exit 1
status=124
if [ -n "$interrupted" ] || [ -n "$startup_failed" ]; then
	[ -z "$startup_failed" ] || status=1
	kill -TERM -- "-$reader_group" 2>/dev/null || :
	sleep 1
else
	read -r status <"$state_dir/result" || status=1
fi
[ "$(jobs -pr)" = "$reader_group" ] || exit 1
kill -0 -- "-$reader_group" 2>/dev/null || exit 1
# Bash can report the deliberately killed job between kill and wait. Keep
# that supervisor notification out of the reader diagnostic stream; the
# reader inherited its original stderr before this cleanup-only redirection.
{
	kill -KILL -- "-$reader_group" || :
	wait "$reader_group" || :
} 2>/dev/null
exit "$status"
' -- "$preflight_state_dir" "$@" &
	preflight_runner_pid=$!
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

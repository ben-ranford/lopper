#!/usr/bin/env bash
# Explicitly instrumented startup controls, separate from exact-helper cases.
set -eu
[ "$#" = 3 ] || { echo 'usage: startup-controls.sh helper observer NEW_EVIDENCE_DIR' >&2; exit 2; }
helper=$1
observer=$2
evidence=$3
[ -f "$helper" ] && [ -f "$observer" ] && [ ! -e "$evidence" ] || exit 2
mkdir -p "$evidence"
evidence=$(cd "$evidence" && pwd -P)
runner=
watchdog=
sentinel=
current=
cleanup() {
  local result=$? anchor=
  trap - EXIT HUP INT TERM
  if [ -n "$current" ] && [ -f "$current/anchor.pid" ]; then
    read -r anchor <"$current/anchor.pid" || anchor=
    case "$anchor" in ''|*[!0-9]*|0|1) ;; *)
      if kill -0 "$anchor" 2>/dev/null; then
        if [ -f "$current/group/verified.txt" ] && grep -q '^expected=owned ' "$current/group/verified.txt"; then
          kill -KILL -- "-$anchor" 2>/dev/null || :
        else
          kill -KILL "$anchor" 2>/dev/null || :
        fi
      fi
      ;;
    esac
  fi
  for pid in "$watchdog" "$runner" "$sentinel"; do
    [ -n "$pid" ] || continue
    kill -TERM "$pid" 2>/dev/null || :
    wait "$pid" 2>/dev/null || :
  done
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
fail() { printf 'FAIL startup control: %s\n' "$*" >&2; exit 1; }
replace_once() {
  local needle=$1 replacement=$2 rest
  rest=${variant_source#*"$needle"}
  [ "$rest" != "$variant_source" ] || fail 'source injection boundary absent'
  case "$rest" in *"$needle"*) fail 'source injection boundary duplicated' ;; esac
  variant_source=${variant_source/"$needle"/"$replacement"}
}
make_variant() {
  local mode=$1 injection restore launch replacement
  variant_source=$(cat "$helper")
  IFS= read -r -d '' injection <<'INJECTION' || :
	while [ ! -f "$state_dir/test-anchor-pid" ]; do :; done
	while ! read -r test_anchor_pid <"$state_dir/test-anchor-pid"; do :; done
	printf "%s\n" "$test_anchor_pid" >"$LOPPER_STARTUP_CASE/anchor.pid"
	while [ ! -f "$LOPPER_STARTUP_CASE/observed.release" ]; do :; done
	case "$LOPPER_STARTUP_MODE" in
		known-warning|known-warning-no-group)
			printf "%s\n" "--: child setpgid ($test_anchor_pid to $test_anchor_pid): Operation not permitted" >&2 ;;
		unknown-warning) printf "unknown startup diagnostic\n" >&2 ;;
		startup-interrupt) kill -TERM "$supervisor_pid" ;;
		exited-child) exit 1 ;;
	esac
INJECTION
  restore=$(printf '\texec 2>&3 3>&-')
  replace_once "$restore" "$injection
$restore"
  IFS= read -r -d '' launch <<'LAUNCH' || :
) & reader_group=$!
LAUNCH
  IFS= read -r -d '' replacement <<'LAUNCH' || :
) & reader_group=$!
printf "%s\n" "$reader_group" >"$state_dir/test-anchor-pid"
LAUNCH
  replace_once "$launch" "$replacement"
  case "$mode" in
    missing-group|known-warning-no-group)
      replace_once "$(printf '\nset -m\n')" "$(printf '\nset +m\n')"
      ;;
  esac
  printf '%s\n' "$variant_source" >"$current/helper.instrumented.sh"
  diff -u "$helper" "$current/helper.instrumented.sh" >"$current/instrumentation.diff" || [ "$?" = 1 ]
}
start_guard() {
  (
    sleeper=
    trap 'if [ -n "$sleeper" ]; then kill "$sleeper" 2>/dev/null || :; wait "$sleeper" 2>/dev/null || :; fi; exit 0' HUP INT TERM
    sleep 18 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    printf expired >"$current/guard-expired"
    kill -TERM "$runner" 2>/dev/null || :
    sleep 3 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    kill -KILL "$runner" 2>/dev/null || :
  ) & watchdog=$!
}
run_control() {
  local mode=$1 expected=$2 ownership=owned status=0 anchor='' started elapsed
  current="$evidence/$mode"
  mkdir -p "$current/state"
  make_variant "$mode"
  printf 'exit 99\n' >"$current/bash-env"
  started=$SECONDS
  TMPDIR="$current/state" BASH_ENV="$current/bash-env" LOPPER_STARTUP_CASE="$current" LOPPER_STARTUP_MODE="$mode" sh -c '
    . "$1"
    trap cleanup_preflight_git EXIT
    trap "exit 143" HUP INT TERM
    printf "%s\n" "$$" >"$2/wrapper.pid"
    run_preflight_git sh -c '\''printf x >"$1/reader.started"; printf "reader output\n"; printf "reader diagnostic\n" >&2; exit 7'\'' sh "$2"
  ' sh "$current/helper.instrumented.sh" "$current" >"$current/stdout" 2>"$current/stderr" &
  runner=$!
  start_guard
  while ! { [ -f "$current/anchor.pid" ] && read -r anchor <"$current/anchor.pid"; }; do
    [ "$((SECONDS - started))" -lt 5 ] || fail "$mode did not publish complete anchor PID"
    sleep 0.01
  done
  case "$anchor" in ''|*[!0-9]*|0|1) fail 'invalid control anchor' ;; esac
  case "$mode" in missing-group|known-warning-no-group) ownership=absent ;; esac
  bash --noprofile --norc -p "$observer" observe none "$anchor" "$runner" "$sentinel" "$current/group" "$ownership" || fail "$mode actual process-group observation failed"
  printf observed >"$current/observed.release"
  wait "$runner" || status=$?
  runner=
  kill -TERM "$watchdog" 2>/dev/null || :
  wait "$watchdog" 2>/dev/null || :
  watchdog=
  elapsed=$((SECONDS - started))
  [ ! -e "$current/guard-expired" ] && [ "$elapsed" -lt 5 ] || fail "$mode exceeded bounded startup"
  [ "$status" = "$expected" ] || fail "$mode status=$status expected=$expected: $(cat "$current/stderr")"
  : >"$current/expected.stdout"
  : >"$current/expected.stderr"
  case "$mode" in
    known-warning)
      [ -f "$current/reader.started" ] || fail 'accepted known warning did not run reader'
      printf 'reader output\n' >"$current/expected.stdout"
      printf 'reader diagnostic\n' >"$current/expected.stderr"
      ;;
    *) [ ! -e "$current/reader.started" ] || fail "$mode authorized a rejected reader" ;;
  esac
  case "$mode" in
    unknown-warning) printf 'unknown startup diagnostic\n' >"$current/expected.stderr" ;;
    known-warning-no-group) printf '%s\n' "--: child setpgid ($anchor to $anchor): Operation not permitted" >"$current/expected.stderr" ;;
  esac
  case "$mode" in missing-group|known-warning-no-group) printf 'Could not establish Git preflight process group\n' >>"$current/expected.stderr" ;; esac
  cmp -s "$current/stdout" "$current/expected.stdout" || fail "$mode changed stdout"
  cmp -s "$current/stderr" "$current/expected.stderr" || fail "$mode changed literal stderr: $(cat "$current/stderr")"
  if kill -0 "$anchor" 2>/dev/null; then fail "$mode left anchor active"; fi
  mv "$current/anchor.pid" "$current/anchor.observed"
  [ -z "$(find "$current/state" -mindepth 1 -print -quit)" ] || fail "$mode left state"
  jobs -pr | grep -qx "$sentinel" || fail "$mode signaled unrelated sentinel"
  printf 'PASS startup %s: status=%s elapsed=%ss reader=%s group=%s anchor=stopped state=empty sentinel=running\n' "$mode" "$status" "$elapsed" "$([ "$mode" = known-warning ] && printf started || printf absent)" "$ownership"
}
sleep 120 & sentinel=$!
bash --noprofile --norc -p "$observer" discover "$evidence/platform" || fail 'actual process-table capabilities missing'
for mode in known-warning unknown-warning missing-group known-warning-no-group startup-interrupt exited-child; do
  expected=1
  [ "$mode" != known-warning ] || expected=7
  [ "$mode" != startup-interrupt ] || expected=124
  run_control "$mode" "$expected"
done
printf 'PASS native startup controls\n'

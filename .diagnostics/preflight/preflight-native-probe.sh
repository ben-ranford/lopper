#!/usr/bin/env bash
# Supplemental native-shell behavior proof; pass the exact checkout's helper path.
set -u
if [ "$#" -ne 2 ] || [ ! -f "$1" ] || [ ! -f "$2" ]; then
  echo 'usage: preflight-native-probe.sh /absolute/path/hook-config-preflight.sh /absolute/path/group-observer.sh' >&2
  exit 2
fi
source_file=$(cd "$(dirname "$1")" && printf '%s/%s\n' "$PWD" "$(basename "$1")")
LOPPER_NATIVE_GROUP_OBSERVER=$(cd "$(dirname "$2")" && printf '%s/%s\n' "$PWD" "$(basename "$2")")
export LOPPER_NATIVE_GROUP_OBSERVER
probe_root=$(mktemp -d "${TMPDIR:-/tmp}/lopper-native-preflight.XXXXXX") || exit 1
evidence_root=${LOPPER_PREFLIGHT_PROBE_EVIDENCE-}
if [ -n "$evidence_root" ]; then
  if [ -e "$evidence_root" ] || ! mkdir -p "$evidence_root"; then
    echo 'Probe evidence path must be new and writable' >&2
    rm -rf "$probe_root"
    exit 2
  fi
fi
runner=
watchdog=
sentinel=
cleanup() {
  local cleanup_status=$?
  trap - EXIT HUP INT TERM
  for pid_file in "$probe_root"/*/pids "$probe_root"/*/anchors; do
    [ -f "$pid_file" ] || continue
    while IFS= read -r pid; do
      case "$pid" in ''|*[!0-9]*|0|1) continue ;; esac
      kill -KILL "$pid" 2>/dev/null || :
    done <"$pid_file"
  done
  for pid in "$watchdog" "$runner" "$sentinel"; do
    [ -n "$pid" ] || continue
    kill -TERM "$pid" 2>/dev/null || :
    wait "$pid" 2>/dev/null || :
  done
  if [ -n "$evidence_root" ]; then
    cp -R "$probe_root"/. "$evidence_root"/ || cleanup_status=1
  fi
  rm -rf "$probe_root"
  exit "$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
process_active() {
  kill -0 "$1" 2>/dev/null || return 1
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) return 0 ;;
    *)
      state=$(ps -p "$1" -o stat= 2>/dev/null) || return 1
      case "$state" in *Z*|'') return 1 ;; *) return 0 ;; esac
      ;;
  esac
}
assert_stopped() {
  local pid_file=$1 pid attempt active
  [ "$(wc -l <"$pid_file" | tr -d '[:space:]')" = 3 ] || fail "incomplete reader PID evidence: $(cat "$pid_file")"
  [ "$(sort -u "$pid_file" | wc -l | tr -d '[:space:]')" = 3 ] || fail 'reader PID evidence is not distinct'
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    active=
    while IFS= read -r pid; do
      case "$pid" in ''|*[!0-9]*|0|1) fail "invalid fixture PID: $pid" ;; esac
      if process_active "$pid"; then active="$active $pid"; fi
    done <"$pid_file"
    [ -n "$active" ] || return 0
    [ "$attempt" -lt 10 ] && sleep 0.1
  done
  fail "reader descendants survived:$active"
}
assert_anchor_stopped() {
  local anchor_file=$1 anchor
  [ "$(wc -l <"$anchor_file" | tr -d '[:space:]')" = 1 ] || fail 'incomplete anchor PID evidence'
  IFS= read -r anchor <"$anchor_file" || fail 'missing anchor PID'
  case "$anchor" in ''|*[!0-9]*|0|1) fail "invalid anchor PID: $anchor" ;; esac
  process_active "$anchor" && fail "anchor still running: $anchor"
  return 0
}
instrument_reader() {
  local fixture=$1
  {
    printf '#!/bin/sh\n'
    cat <<'OBSERVER'
bash --noprofile --norc -p "$LOPPER_NATIVE_GROUP_OBSERVER" observe "$$" "$PPID" "$LOPPER_PROBE_WRAPPER_PID" "$LOPPER_NATIVE_SENTINEL_PID" "${1%/pids}/group" owned || exit 92
OBSERVER
    tail -n +2 "$fixture"
  } >"$fixture.observed"
  mv "$fixture.observed" "$fixture" || fail 'install reader observer'
}
run_case() {
  local case_name=$1 expected=$2 mode=${3:-timeout} case_dir status started elapsed
  case_dir="$probe_root/$case_name"
  mkdir -p "$case_dir/state" || fail 'create fixture state'
  cat >"$case_dir/reader" || fail 'write fixture'
  instrument_reader "$case_dir/reader"
  chmod +x "$case_dir/reader" || fail 'make fixture executable'
  # Poison only the supervisor's startup environment, never interpolate argv.
  printf 'exit 99\n' >"$case_dir/bash-env"
  started=$SECONDS
  TMPDIR="$case_dir/state" BASH_ENV="$case_dir/bash-env" sh -c '
    . "$1"
    trap cleanup_preflight_git EXIT
    trap "exit 143" HUP INT TERM
    LOPPER_PROBE_WRAPPER_PID=$$
    export LOPPER_PROBE_WRAPPER_PID
    run_preflight_git "$2" "$3" "$4" "$5"
  ' sh "$source_file" "$case_dir/reader" "$case_dir/pids" "$case_dir/anchors" "literal \$(exit 88) 'argument'" >"$case_dir/output" 2>&1 &
  runner=$!
  (
    sleeper=
    trap 'if [ -n "$sleeper" ]; then kill "$sleeper" 2>/dev/null || :; wait "$sleeper" 2>/dev/null || :; fi; exit 0' HUP INT TERM
    sleep 18 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    printf 'expired\n' >"$case_dir/guard-expired"
    kill -TERM "$runner" 2>/dev/null || :
    sleep 3 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    kill -KILL "$runner" 2>/dev/null || :
  ) & watchdog=$!
  status=0
  wait "$runner" || status=$?
  runner=
  kill -TERM "$watchdog" 2>/dev/null || :
  wait "$watchdog" 2>/dev/null || :
  watchdog=
  elapsed=$((SECONDS - started))
  [ ! -f "$case_dir/guard-expired" ] || fail "$case_name exceeded outer 18-second guard"
  [ "$status" = "$expected" ] || fail "$case_name status=$status expected=$expected: $(cat "$case_dir/output")"
  if [ "$expected" = 124 ]; then
    if [ "$mode" = timeout ]; then
      grep -q '^Timed out while reading Git preflight configuration$' "$case_dir/output" || fail "$case_name lost timeout diagnostic"
    else
      [ ! -s "$case_dir/output" ] || fail "$case_name changed interruption output: $(cat "$case_dir/output")"
      [ "$elapsed" -lt 5 ] || fail "$case_name interruption took ${elapsed}s"
    fi
  else
    printf '%s\nreader diagnostic\n' "literal \$(exit 88) 'argument'" >"$case_dir/expected"
    cmp -s "$case_dir/expected" "$case_dir/output" || fail "$case_name changed reader output: $(cat "$case_dir/output")"
    [ "$elapsed" -lt 5 ] || fail "$case_name normal result took ${elapsed}s"
  fi
  assert_stopped "$case_dir/pids"
  assert_anchor_stopped "$case_dir/anchors"
  mv "$case_dir/pids" "$case_dir/pids.observed" || fail 'retain reader PID evidence'
  mv "$case_dir/anchors" "$case_dir/anchors.observed" || fail 'retain anchor PID evidence'
  [ -z "$(find "$case_dir/state" -mindepth 1 -print -quit)" ] || fail "$case_name left temporary state"
  # jobs -r rejects a killed-but-unreaped sentinel as well as an exited one.
  jobs -pr | grep -qx "$sentinel" || fail "$case_name signaled unrelated sentinel"
  printf 'PASS %s: status=%s elapsed=%ss descendants=0 state=empty sentinel=running\n' "$case_name" "$status" "$elapsed"
}
sleep 120 & sentinel=$!
LOPPER_NATIVE_SENTINEL_PID=$sentinel
export LOPPER_NATIVE_SENTINEL_PID
bash --noprofile --norc -p "$LOPPER_NATIVE_GROUP_OBSERVER" discover "$probe_root/platform" || fail 'native process-group capabilities unavailable'
printf 'Native preflight probe: %s; %s\n' "$(uname -s)" "$BASH_VERSION"
run_case term-spawn 124 <<'READER'
#!/bin/sh
printf '%s\n' "$PPID" >> "$2"
trap 'sleep 60 & printf "%s\n" "$!" >> "$1"; exit' TERM
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
wait
READER
run_case term-ignore 124 <<'READER'
#!/bin/sh
printf '%s\n' "$PPID" >> "$2"
trap '' TERM
printf '%s\n' "$$" >> "$1"
sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$1"; sleep 60 & printf "%s\n" "$!" >> "$1"; wait' sh "$1" &
wait
READER
run_case normal-result 7 <<'READER'
#!/bin/sh
printf '%s\n' "$PPID" >> "$2"
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
printf '%s\n' "$3"
printf 'reader diagnostic\n' >&2
exit 7
READER
run_case parent-interrupt 124 interrupt <<'READER'
#!/bin/sh
printf '%s\n' "$PPID" >> "$2"
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
kill -TERM "$PPID"
wait
READER
start_config_guard() {
  local guard_file=$1
  (
    sleeper=
    trap 'if [ -n "$sleeper" ]; then kill "$sleeper" 2>/dev/null || :; wait "$sleeper" 2>/dev/null || :; fi; exit 0' HUP INT TERM
    sleep 18 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    printf 'expired\n' >"$guard_file"
    kill -TERM "$runner" 2>/dev/null || :
    sleep 3 & sleeper=$!
    wait "$sleeper" || exit 0
    sleeper=
    kill -KILL "$runner" 2>/dev/null || :
  ) & watchdog=$!
}
wait_config_guard() {
  local guard_file=$1 guarded_status=0
  wait "$runner" || guarded_status=$?
  runner=
  kill -TERM "$watchdog" 2>/dev/null || :
  wait "$watchdog" 2>/dev/null || :
  watchdog=
  [ ! -f "$guard_file" ] || fail 'config command exceeded outer 18-second guard'
  return "$guarded_status"
}
run_config_case() {
  local case_name=$1 expected=$2 attempt=$3 case_dir status direct_status started elapsed
  case_dir="$probe_root/$case_name-$attempt"
  mkdir -p "$case_dir/state" || fail 'create config fixture state'
  if [ "$case_name" = absent-config ]; then
    : >"$case_dir/config"
  else
    printf '[broken\n' >"$case_dir/config"
  fi
  direct_status=0
  LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null "$git_executable" config --file "$case_dir/config" --get fixture.absent >"$case_dir/expected.stdout" 2>"$case_dir/expected.stderr" &
  runner=$!
  start_config_guard "$case_dir/direct-guard-expired"
  wait_config_guard "$case_dir/direct-guard-expired" || direct_status=$?
  [ "$direct_status" = "$expected" ] || fail "$case_name direct Git status=$direct_status expected=$expected"
  [ ! -s "$case_dir/expected.stdout" ] || fail "$case_name direct Git unexpectedly produced stdout"
  if [ "$case_name" = absent-config ]; then
    [ ! -s "$case_dir/expected.stderr" ] || fail 'absent config direct Git produced stderr'
  else
    [ -s "$case_dir/expected.stderr" ] || fail 'invalid config direct Git did not produce its real error'
  fi
  cat >"$case_dir/reader" <<'READER'
#!/bin/sh
printf '%s\n' "$PPID" >> "$2"
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
exec "$3" config --file "${1%/pids}/config" --get fixture.absent
READER
  instrument_reader "$case_dir/reader"
  chmod +x "$case_dir/reader" || fail 'make config fixture executable'
  printf 'exit 99\n' >"$case_dir/bash-env"
  started=$SECONDS
  LC_ALL=C GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null TMPDIR="$case_dir/state" BASH_ENV="$case_dir/bash-env" sh -c '
    . "$1"
    trap cleanup_preflight_git EXIT
    trap "exit 143" HUP INT TERM
    LOPPER_PROBE_WRAPPER_PID=$$
    export LOPPER_PROBE_WRAPPER_PID
    run_preflight_git "$2" "$3" "$4" "$5"
  ' sh "$source_file" "$case_dir/reader" "$case_dir/pids" "$case_dir/anchors" "$git_executable" >"$case_dir/actual.stdout" 2>"$case_dir/actual.stderr" &
  runner=$!
  start_config_guard "$case_dir/guard-expired"
  status=0
  wait_config_guard "$case_dir/guard-expired" || status=$?
  elapsed=$((SECONDS - started))
  [ ! -f "$case_dir/guard-expired" ] || fail "$case_name exceeded outer 18-second guard"
  [ "$status" = "$expected" ] || fail "$case_name status=$status expected=$expected: $(cat "$case_dir/actual.stderr")"
  cmp -s "$case_dir/expected.stdout" "$case_dir/actual.stdout" || fail "$case_name changed Git stdout"
  cmp -s "$case_dir/expected.stderr" "$case_dir/actual.stderr" || fail "$case_name changed Git stderr: $(cat "$case_dir/actual.stderr")"
  [ "$elapsed" -lt 5 ] || fail "$case_name normal result took ${elapsed}s"
  assert_stopped "$case_dir/pids"
  assert_anchor_stopped "$case_dir/anchors"
  mv "$case_dir/pids" "$case_dir/pids.observed" || fail 'retain reader PID evidence'
  mv "$case_dir/anchors" "$case_dir/anchors.observed" || fail 'retain anchor PID evidence'
  [ -z "$(find "$case_dir/state" -mindepth 1 -print -quit)" ] || fail "$case_name left temporary state"
  jobs -pr | grep -qx "$sentinel" || fail "$case_name signaled unrelated sentinel"
  printf 'PASS %s: attempt=%s status=%s elapsed=%ss stdout=exact stderr=exact descendants=0 state=empty sentinel=running\n' "$case_name" "$attempt" "$status" "$elapsed"
}
# Apple's /usr/bin/git launcher writes xcrun_db into TMPDIR. Resolve the actual
# Git binary before creating private helper state; do not weaken its empty check.
case "$(uname -s)" in
  Darwin) git_executable=$(xcrun --find git) || fail 'resolve actual Darwin Git' ;;
  *) git_executable=$(command -v git) || fail 'resolve native Git' ;;
esac
[ -x "$git_executable" ] || fail 'resolved Git is not executable'
printf 'Config probe Git: %s; %s\n' "$git_executable" "$("$git_executable" --version)"
for config_attempt in 1 2 3 4 5; do
  run_config_case absent-config 1 "$config_attempt"
  run_config_case invalid-config 128 "$config_attempt"
done
printf 'PASS native preflight probe\n'

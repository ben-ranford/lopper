#!/usr/bin/env bash
# Supplemental native-shell behavior proof; pass the exact checkout's helper path.
set -u
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  echo 'usage: preflight-native-probe.sh /absolute/path/hook-config-preflight.sh' >&2
  exit 2
fi
source_file=$(cd "$(dirname "$1")" && printf '%s/%s\n' "$PWD" "$(basename "$1")")
probe_root=$(mktemp -d "${TMPDIR:-/tmp}/lopper-native-preflight.XXXXXX") || exit 1
runner=
watchdog=
sentinel=
cleanup() {
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
  rm -rf "$probe_root"
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
run_case() {
  local case_name=$1 expected=$2 mode=${3:-timeout} case_dir status started elapsed
  case_dir="$probe_root/$case_name"
  mkdir -p "$case_dir/state" || fail 'create fixture state'
  cat >"$case_dir/reader" || fail 'write fixture'
  chmod +x "$case_dir/reader" || fail 'make fixture executable'
  # Poison only the supervisor's startup environment, never interpolate argv.
  printf 'exit 99\n' >"$case_dir/bash-env"
  started=$SECONDS
  TMPDIR="$case_dir/state" BASH_ENV="$case_dir/bash-env" sh -c '
    . "$1"
    trap cleanup_preflight_git EXIT
    trap "exit 143" HUP INT TERM
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
  while IFS= read -r anchor; do
    process_active "$anchor" && fail "$case_name left its anchor running: $anchor"
  done <"$case_dir/anchors"
  rm -f "$case_dir/pids" "$case_dir/anchors"
  [ -z "$(find "$case_dir/state" -mindepth 1 -print -quit)" ] || fail "$case_name left temporary state"
  # jobs -r rejects a killed-but-unreaped sentinel as well as an exited one.
  jobs -pr | grep -qx "$sentinel" || fail "$case_name signaled unrelated sentinel"
  printf 'PASS %s: status=%s elapsed=%ss descendants=0 state=empty sentinel=running\n' "$case_name" "$status" "$elapsed"
}
sleep 120 & sentinel=$!
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
printf 'PASS native preflight probe\n'

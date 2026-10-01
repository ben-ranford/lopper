#!/usr/bin/env bash
# Independent process-table observer. No signals except existence probes (signal0).
set -eu
fail() { printf 'Group observer: %s\n' "$*" >&2; exit 1; }
numeric() { case "$1" in ''|*[!0-9]*|0|1) fail "invalid PID $1" ;; esac; }
process_row() {
  local pid=$1 destination=$2
  numeric "$pid"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) ps -l -p "$pid" >"$destination" ;;
    Darwin) ps -p "$pid" -o pid,ppid,pgid >"$destination" ;;
    *) fail 'unsupported process-table platform' ;;
  esac
  # Discover named columns from the actual header. Cygwin documents an optional
  # leading S/I/O row status flag; no fixed numeric column positions are assumed.
  awk -v wanted="$pid" '
    NR==1 {
      for(i=1;i<=NF;i++) {
        if($i=="PID") {pid=i;pc++}
        if($i=="PPID") {ppid=i;ppc++}
        if($i=="PGID") {pgid=i;pgc++}
      }
      if(pc!=1 || ppc!=1 || pgc!=1) exit 2
      next
    }
    NF {
      offset=($1 ~ /^[SIO]$/) ? 1 : 0
      p=$(pid+offset);pp=$(ppid+offset);pg=$(pgid+offset)
      if(p !~ /^[0-9]+$/ || pp !~ /^[0-9]+$/ || pg !~ /^[0-9]+$/ || p!=wanted) exit 3
      rows++;result=p " " pp " " pg
    }
    END {if(rows!=1) exit 4; print result}
  ' "$destination" >"$destination.parsed" || fail 'unsupported or ambiguous actual ps record'
}
discover() {
  local destination=$1
  mkdir -p "$destination"
  printf '%s\n' "$(uname -s)" >"$destination/platform.txt"
  command -v ps >"$destination/ps-path.txt"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      ps --help >"$destination/ps-help.txt" 2>&1 || fail 'native ps help unavailable'
      ps --version >"$destination/ps-version.txt" 2>&1 || fail 'native ps version unavailable'
      grep -q -- '--long' "$destination/ps-help.txt" || fail 'native ps lacks documented long form'
      grep -q -- '--process' "$destination/ps-help.txt" || fail 'native ps lacks PID selection'
      printf 'MSYS/Cygwin POSIX process-group semantics; not Windows kernel PGIDs\n' >"$destination/semantics.txt"
      ;;
    Darwin) printf 'Darwin kernel POSIX process-group semantics\n' >"$destination/semantics.txt" ;;
    *) fail 'unsupported platform' ;;
  esac
  process_row "$$" "$destination/self.ps"
  read -r _ _ observed_group <"$destination/self.ps.parsed"
  numeric "$observed_group"
  kill -0 -- "-$observed_group" || fail 'negative group signal0 unsupported'
  printf 'negative-group-signal0=PASS\n' >"$destination/kill-semantics.txt"
}
observe() {
  local reader=$1 anchor=$2 wrapper=$3 sentinel=$4 destination=$5 expected=$6
  local observed_pid parent group anchor_group supervisor wrapper_group sentinel_group
  mkdir -p "$destination"
  process_row "$anchor" "$destination/anchor.ps"
  read -r observed_pid supervisor anchor_group <"$destination/anchor.ps.parsed"
  [ "$observed_pid" = "$anchor" ] || fail 'anchor record PID differs'
  process_row "$supervisor" "$destination/supervisor.ps"
  process_row "$wrapper" "$destination/wrapper.ps"
  read -r _ _ wrapper_group <"$destination/wrapper.ps.parsed"
  process_row "$sentinel" "$destination/sentinel.ps"
  read -r _ _ sentinel_group <"$destination/sentinel.ps.parsed"
  if [ "$expected" = owned ]; then
    [ "$anchor_group" = "$anchor" ] || fail 'anchor does not own its reported group'
    [ "$wrapper_group" != "$anchor" ] && [ "$sentinel_group" != "$anchor" ] || fail 'unrelated process shares the reader group'
    read -r _ _ group <"$destination/supervisor.ps.parsed"
    [ "$group" != "$anchor" ] || fail 'supervisor is in reader group'
    kill -0 -- "-$anchor" || fail 'owned negative group is absent'
    printf 'negative-group-signal0=PASS\n' >"$destination/kill-semantics.txt"
    if [ "$reader" != none ]; then
      process_row "$reader" "$destination/reader.ps"
      read -r observed_pid parent group <"$destination/reader.ps.parsed"
      [ "$observed_pid" = "$reader" ] && [ "$parent" = "$anchor" ] && [ "$group" = "$anchor" ] || fail 'reader parent or group differs from anchor'
    fi
  elif [ "$expected" = absent ]; then
    [ "$reader" = none ] && [ "$anchor_group" != "$anchor" ] || fail 'missing-group control unexpectedly owns a group'
    if kill -0 -- "-$anchor" 2>/dev/null; then fail 'missing-group signal0 unexpectedly passed'; fi
    printf 'negative-group-signal0=ABSENT\n' >"$destination/kill-semantics.txt"
  else
    fail 'unknown group expectation'
  fi
  printf 'expected=%s reader=%s anchor=%s supervisor=%s wrapper=%s sentinel=%s\n' "$expected" "$reader" "$anchor" "$supervisor" "$wrapper" "$sentinel" >"$destination/verified.txt"
}
case "${1-}" in
  discover) [ "$#" = 2 ] || fail 'discover arguments'; discover "$2" ;;
  observe) [ "$#" = 7 ] || fail 'observe arguments'; shift; observe "$@" ;;
  *) fail 'expected discover or observe' ;;
esac

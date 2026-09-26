#!/bin/sh
# Remove only an unreferenced managed snapshot. Any failed inspection retains it.
set -eu
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_PREFIX
script=$0
case "$script" in /*) ;; *) script=$PWD/$script ;; esac
# shellcheck source=scripts/hook-config-preflight.sh
. "${script%/*}/hook-config-preflight.sh"
trap cleanup_preflight_git EXIT

check_reference() {
 candidate=$2
 # Drive-letter/UNC paths can only be classified on a Windows Git host.
 case "$candidate" in
  [A-Za-z]:/*|[A-Za-z]:\\*|\\\\*|//*)
   case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) ;; *) return 255 ;; esac
   candidate=$(printf '%s' "$candidate" | tr '\134' '/' && printf x) || return 255
   candidate=${candidate%x}
   ;;
  # Drive-relative paths depend on per-drive working directories. Retain the
  # snapshot when that context cannot be resolved unambiguously.
  [A-Za-z]:*) return 255 ;;
  /*) ;;
  *) candidate=$PWD/$candidate ;;
 esac
 resolved=$(cd "$candidate" && pwd -P && printf x) || return 255
 resolved=${resolved%x}; resolved=${resolved%?}
 managed=$(cd "$1" && pwd -P && printf x) || return 255
 managed=${managed%x}; managed=${managed%?}
 [ ! "$resolved" -ef "$managed" ] || return 255
}

check_worktree() {
 managed_dir=$1
 reference_file=$2
 worktree_record=$3
 case "$worktree_record" in
 "worktree "*)
  printf x
  root=${worktree_record#worktree }
  cd "$root" || return 255
  # A stale inventory entry may now name an unrelated repository. Verify its
  # backpointer before trusting configuration read from that directory.
  owner=$(run_preflight_git git rev-parse --path-format=absolute --git-common-dir && printf x) || return 255
  owner=${owner%x}; owner=${owner%?}
  [ "$owner" -ef "${managed_dir%/lopper-hooks}" ] || return 255
  status=0
  run_preflight_git git -c core.fsmonitor=false config --path --null --get core.hooksPath >"$reference_file" || status=$?
  case "$status" in 0) ;; 1) return 0 ;; *) return 255 ;; esac
  xargs -0 -n 1 sh "$script" reference "$managed_dir" <"$reference_file" || return 255
  ;;
 *) return 0 ;;
 esac
}

if [ "${1-}" = reference ]; then
	shift
	check_reference "$@"
	exit
fi
if [ "${1-}" = worktree ]; then
	shift
	check_worktree "$@"
	exit
fi
if [ -n "${1-}" ]; then exit 0; fi

common_dir=$(run_preflight_git git rev-parse --path-format=absolute --git-common-dir && printf x) || exit 0
common_dir=${common_dir%x}
common_dir=${common_dir%?}
managed_dir=$common_dir/lopper-hooks
[ -d "$managed_dir" ] && [ ! -L "$managed_dir" ] || exit 0
inventory=$(mktemp) || exit 0
references=$(mktemp) || { rm -f "$inventory"; exit 0; }
cleanup() { cleanup_preflight_git; rm -f "$inventory" "$references"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# Porcelain -z preserves spaces and newlines in worktree paths. Git evaluates
# conditional includes with the owning worktree's gitdir and branch context.
run_preflight_git git worktree list --porcelain -z >"$inventory" || exit 0
[ -s "$inventory" ] || exit 0
inspected=$(xargs -0 -n 1 sh "$script" worktree "$managed_dir" "$references" <"$inventory") || exit 0
[ -n "$inspected" ] || exit 0
# Never recursively delete the directory or remove unexpected user-owned files.
[ -f "$managed_dir/pre-commit" ] && [ ! -L "$managed_dir/pre-commit" ] || exit 0
rm -f "$managed_dir/pre-commit"
rmdir "$managed_dir" 2>/dev/null || :

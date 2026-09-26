#!/bin/sh
# Remove only an unreferenced managed snapshot. Any failed inspection retains it.
set -eu
requested_managed_dir=${LOPPER_CLEANUP_MANAGED_DIR-}
requested_common_dir=${LOPPER_CLEANUP_COMMON_DIR-}
requested_git_dir=${LOPPER_CLEANUP_GIT_DIR-}
while [ "$#" -gt 0 ]; do
	case "$1" in
		--managed-dir) [ "$#" -ge 2 ] || exit 2; requested_managed_dir=$2; shift 2 ;;
		--common-dir) [ "$#" -ge 2 ] || exit 2; requested_common_dir=$2; shift 2 ;;
		--git-dir) [ "$#" -ge 2 ] || exit 2; requested_git_dir=$2; shift 2 ;;
		*) break ;;
	esac
done
case "$requested_managed_dir" in '') ;; /*/lopper-hooks) LOPPER_CLEANUP_MANAGED_DIR=$requested_managed_dir; export LOPPER_CLEANUP_MANAGED_DIR ;; *) exit 0 ;; esac
case "$requested_common_dir" in '') ;; /*) LOPPER_CLEANUP_COMMON_DIR=$requested_common_dir; export LOPPER_CLEANUP_COMMON_DIR ;; *) exit 0 ;; esac
case "$requested_git_dir" in '') ;; /*) LOPPER_CLEANUP_GIT_DIR=$requested_git_dir; export LOPPER_CLEANUP_GIT_DIR ;; *) exit 0 ;; esac
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_PREFIX
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
script=$0
case "$script" in /*) ;; *) script=$PWD/$script ;; esac
# shellcheck source=scripts/hook-config-preflight.sh
. "${script%/*}/hook-config-preflight.sh"
trap cleanup_preflight_git EXIT

file_identity() {
 # Both stat variants follow links. Reject errors or unexpected output instead
 # of treating an uninspectable file as distinct from the managed snapshot.
 identity=$(stat -L -c '%d:%i' "$1" 2>/dev/null) ||
  identity=$(stat -L -f '%d:%i' "$1" 2>/dev/null) || return 255
 case "$identity" in
  ''|*[!0-9:]*|:*|*:|*:*:*) return 255 ;;
  *:*) printf '%s\n' "$identity" ;;
  *) return 255 ;;
 esac
}

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
 reference_identity=$(file_identity "$resolved") || return 255
 managed_identity=$(file_identity "$managed") || return 255
 [ "$reference_identity" != "$managed_identity" ] || return 255
 # A custom hook directory may still execute the snapshot through a file link.
 if [ -e "$resolved/pre-commit" ] || [ -L "$resolved/pre-commit" ]; then
  reference_identity=$(file_identity "$resolved/pre-commit") || return 255
  managed_identity=$(file_identity "$managed/pre-commit") || return 255
  [ "$reference_identity" != "$managed_identity" ] || return 255
 fi
}

hook_working_directory() {
 bare=$(run_preflight_git git rev-parse --is-bare-repository) || return 255
 case "$bare" in
  true) run_preflight_git git rev-parse --absolute-git-dir ;;
  false) run_preflight_git git rev-parse --show-toplevel ;;
  *) return 255 ;;
 esac
}

check_worktree() {
 managed_dir=$1
 reference_file=$2
 worktree_record=$3
 case "$worktree_record" in
 "worktree "*)
  printf x
 root=${worktree_record#worktree }
	if [ "${LOPPER_CLEANUP_ALT_INVENTORY-}" = 1 ]; then
		root_identity=$(file_identity "$root") || return 255
		target_identity=$(file_identity "${managed_dir%/lopper-hooks}") || return 255
		[ "$root_identity" != "$target_identity" ] || return 0
	fi
  cd "$root" || return 255
  # A stale inventory entry may now name an unrelated repository. Verify its
  # backpointer before trusting configuration read from that directory.
  owner=$(run_preflight_git git rev-parse --path-format=absolute --git-common-dir && printf x) || return 255
  owner=${owner%x}; owner=${owner%?}
  owner_identity=$(file_identity "$owner") || return 255
	source_identity=$(file_identity "$LOPPER_CLEANUP_SOURCE_COMMON_DIR") || return 255
	target_identity=$(file_identity "${managed_dir%/lopper-hooks}") || return 255
	[ "$owner_identity" = "$source_identity" ] || [ "$owner_identity" = "$target_identity" ] || return 255
	inspect_worktree_hooks "$managed_dir" "$reference_file" || return 255
	;;
 *) return 0 ;;
 esac
}

inspect_worktree_hooks() {
	managed_dir=$1
	reference_file=$2
	status=0
	run_preflight_git git -c core.fsmonitor=false config --path --null --get core.hooksPath >"$reference_file" || status=$?
	case "$status" in
		0) ;;
		1)
			# An unset override still executes hooks from Git's default directory.
			# Resolve it in this worktree's context before checking file identity.
			default_hooks=$(run_preflight_git git rev-parse --path-format=absolute --git-path hooks && printf x) || return 255
			default_hooks=${default_hooks%x}; default_hooks=${default_hooks%?}
			check_reference "$managed_dir" "$default_hooks"
			return $?
			;;
		*) return 255 ;;
	esac
	# Relative hook paths use Git's effective worktree root, which core.worktree
	# can redirect away from the directory recorded in the worktree inventory.
	hook_root=$(hook_working_directory && printf x) || return 255
	hook_root=${hook_root%x}; hook_root=${hook_root%?}
	cd "$hook_root" || return 255
	xargs -0 -n 1 sh "$script" reference "$managed_dir" <"$reference_file" || return 255
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

if [ -n "$requested_managed_dir" ]; then
	case "$requested_managed_dir" in /*/lopper-hooks) managed_dir=$requested_managed_dir ;; *) exit 0 ;; esac
else
	common_dir=$(run_preflight_git git rev-parse --path-format=absolute --git-common-dir && printf x) || exit 0
	common_dir=${common_dir%x}
	common_dir=${common_dir%?}
	managed_dir=$common_dir/lopper-hooks
fi
[ -d "$managed_dir" ] && [ ! -L "$managed_dir" ] || exit 0
source_common_dir=$(run_preflight_git git rev-parse --path-format=absolute --git-common-dir && printf x) || exit 0
source_common_dir=${source_common_dir%x}; source_common_dir=${source_common_dir%?}
LOPPER_CLEANUP_SOURCE_COMMON_DIR=$source_common_dir
export LOPPER_CLEANUP_SOURCE_COMMON_DIR
inventory=$(mktemp) || exit 0
alternate_inventory=$(mktemp) || { rm -f "$inventory"; exit 0; }
references=$(mktemp) || { rm -f "$inventory"; exit 0; }
cleanup() { cleanup_preflight_git; rm -f "$inventory" "$alternate_inventory" "$references"; }
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
if [ -n "$requested_common_dir" ] && [ -n "$requested_git_dir" ] && [ "$(file_identity "$requested_common_dir")" != "$(file_identity "$source_common_dir")" ]; then
	GIT_DIR=$requested_git_dir GIT_COMMON_DIR=$requested_common_dir run_preflight_git git worktree list --porcelain -z >"$alternate_inventory" || exit 0
	[ -s "$alternate_inventory" ] || exit 0
	LOPPER_CLEANUP_ALT_INVENTORY=1
	export LOPPER_CLEANUP_ALT_INVENTORY
	alternate_inspected=$(xargs -0 -n 1 sh "$script" worktree "$managed_dir" "$references" <"$alternate_inventory") || exit 0
	unset LOPPER_CLEANUP_ALT_INVENTORY
	[ -n "$alternate_inspected" ] || exit 0
fi
# Never recursively delete the directory or remove unexpected user-owned files.
[ -f "$managed_dir/pre-commit" ] && [ ! -L "$managed_dir/pre-commit" ] || exit 0
rm -f "$managed_dir/pre-commit"
rmdir "$managed_dir" 2>/dev/null || :

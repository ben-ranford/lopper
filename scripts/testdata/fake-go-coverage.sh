#!/bin/sh
set -eu

cmd="${1:-}"
if [ -z "$cmd" ]; then
	echo "missing command" >&2
	exit 1
fi
shift

case "$cmd" in
	env)
		case "${1:-}" in
			GOOS) echo darwin ;;
			GOARCH) echo arm64 ;;
			*)
				echo "unexpected env query: ${1:-}" >&2
				exit 1
				;;
		esac
		;;
	list)
		printf '%s\n' ./cmd/lopper ./internal/app ./internal/testutil ./internal/testsupport ./tools/benchdelta
		;;
	test)
		coverprofile=
		for arg in "$@"; do
			case "$arg" in
				-coverprofile=*)
					coverprofile="${arg#-coverprofile=}"
					;;
				*)
					:
					;;
			esac
		done
		if [ -z "$coverprofile" ]; then
			echo "missing -coverprofile" >&2
			exit 1
		fi
		parent=$(dirname "$coverprofile")
		if [ ! -d "$parent" ]; then
			echo "missing coverprofile parent: $parent" >&2
			exit 91
		fi
		printf 'mode: atomic\npkg/file.go:1.1,1.2 1 1\n' > "$coverprofile"
		;;
	run)
		if [ "${1:-}" != "./tools/coveragegate" ]; then
			echo "unexpected go run target: ${1:-}" >&2
			exit 1
		fi
		shift
		coverprofile=
		for arg in "$@"; do
			case "$arg" in
				-coverprofile=*)
					coverprofile="${arg#-coverprofile=}"
					;;
				-total-out=*|-packages-out=*|-package-failures-out=*)
					output="${arg#*=}"
					parent=$(dirname "$output")
					if [ ! -d "$parent" ]; then
						echo "missing coveragegate output parent: $parent" >&2
						exit 92
					fi
					: > "$output"
					;;
				*)
					:
					;;
			esac
		done
		if [ -z "$coverprofile" ] || [ ! -f "$coverprofile" ]; then
			echo "missing coverage profile for coveragegate: $coverprofile" >&2
			exit 93
		fi
		;;
	*)
		echo "unexpected go command: $cmd" >&2
		exit 1
		;;
esac

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ "$#" -lt 1 ]]; then
	echo "Usage: $0 <output-path>" >&2
	exit 1
fi

OUT_PATH="$1"
OUT_DIR="$(dirname "$OUT_PATH")"
mkdir -p "$OUT_DIR"
MANPAGE_DATE="$(date -u +%Y-%m-%d)"

USAGE_TEXT="$(awk '
	/const usage = `/ { in_usage = 1; next }
	in_usage && /^`$/ { in_usage = 0; exit }
	in_usage { print }
' internal/cli/usage.go)"

if [[ -z "$USAGE_TEXT" ]]; then
	echo "Failed to read usage text from internal/cli/usage.go" >&2
	exit 1
fi

escape_roff() {
	local text="$1"
	printf '%s' "$text" | sed 's/\\/\\\\/g'
	return 0
}

{
	printf '.TH LOPPER 1 "%s" "lopper" "User Commands"\n' "$MANPAGE_DATE"
	printf ".SH NAME\n"
	printf "lopper \\- local-first CLI/TUI for dependency surface analysis\n"
	printf ".SH SYNOPSIS\n"
	printf ".nf\n"
	printf "lopper [--version] [tui]\n"
	printf "lopper tui [--repo PATH] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart]\n"
	printf "lopper analyse --top N [options]\n"
	printf "lopper analyse <dependency> [options]\n"
	printf "lopper dashboard [--repos PATH1,PATH2 | --config PATH]\n"
	printf ".fi\n"
	printf ".SH DESCRIPTION\n"
	printf "This man page is generated from the built-in command usage output.\n"
	printf ".PP\n"
	printf "Usage and options:\n"
	printf ".nf\n"
	while IFS= read -r line; do
		escape_roff "$line"
		printf "\n"
	done <<< "$USAGE_TEXT"
	printf ".fi\n"
	printf ".SH SEE ALSO\n"
	printf "The lopper project homepage and release notes are published at:\n"
	printf ".I https://github.com/ben-ranford/lopper\n"
	printf ".SH BUGS\n"
	printf "Report issues at:\n"
	printf ".I https://github.com/ben-ranford/lopper/issues\n"
} > "$OUT_PATH"

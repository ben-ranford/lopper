#!/usr/bin/env bash
set -euo pipefail

error() {
  echo "::error::$*" >&2
}

trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

lower() {
  local value="$1"
  printf '%s' "$value" | tr '[:upper:]' '[:lower:]'
}

write_output() {
  local name="$1"
  local value="$2"
  if [[ -z "${GITHUB_OUTPUT:-}" ]]; then
    return
  fi
  if [[ "$value" == *$'\n'* || "$value" == *$'\r'* ]]; then
    local delimiter="lopper_${name}_EOF"
    while [[ "$value" == *"$delimiter"* ]]; do
      delimiter="${delimiter}_x"
    done
    {
      printf '%s<<%s\n' "$name" "$delimiter"
      printf '%s\n' "$value"
      printf '%s\n' "$delimiter"
    } >> "$GITHUB_OUTPUT"
    return
  fi
  printf '%s=%s\n' "$name" "$value" >> "$GITHUB_OUTPUT"
}

curl_with_token() {
  local token
  token="$(trim "${LOPPER_GITHUB_TOKEN:-}")"
  local curl_args=()
  if [[ -n "$token" ]]; then
    curl_args=(-H "Authorization: Bearer ${token}")
  fi
  if [[ "${#curl_args[@]}" -gt 0 ]]; then
    curl "${curl_args[@]}" "$@"
    return
  fi
  curl "$@"
}

resolve_latest_tag() {
  local effective_url
  effective_url="$(curl_with_token -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/ben-ranford/lopper/releases/latest")"
  local tag="${effective_url##*/}"
  tag="${tag%%\?*}"
  tag="${tag%%#*}"
  if [[ -z "$tag" || "$tag" == "latest" ]]; then
    error "Unable to resolve the latest Lopper release tag."
    exit 1
  fi
  printf '%s' "$tag"
}

is_concrete_release_ref() {
  local value="$1"
  [[ "$value" =~ ^v[0-9]+[.][0-9]+[.][0-9]+([.-][A-Za-z0-9._-]+)?$ || "$value" =~ ^rolling-[A-Za-z0-9._-]+$ ]]
}

is_floating_release_ref() {
  local value="$1"
  [[ "$value" =~ ^v[0-9]+([.][0-9]+)?$ ]]
}

normalize_explicit_version() {
  local value="$1"
  if [[ "$value" =~ ^[0-9]+([.][0-9]+)?([.][0-9]+([.-][A-Za-z0-9._-]+)?)?$ ]]; then
    value="v${value}"
  fi
  printf '%s' "$value"
}

validate_tag() {
  local tag="$1"
  if [[ ! "$tag" =~ ^[A-Za-z0-9._-]+$ ]]; then
    error "Invalid Lopper version '${tag}'. Use latest, action, a floating tag such as v1 or v1.7, or a release tag such as v1.7.0."
    exit 2
  fi
}

resolve_release_series_tag() {
  local series="$1"

  if ! command -v python3 >/dev/null 2>&1; then
    error "python3 is required to resolve floating Lopper release ref '${series}'. Use a concrete tag such as v1.7.0 instead."
    exit 1
  fi

  local releases_json
  releases_json="$(curl_with_token -fsSL "https://api.github.com/repos/ben-ranford/lopper/releases?per_page=100")"

  local tag
  if ! tag="$(
    SERIES="$series" RELEASES_JSON="$releases_json" python3 - <<'PY'
import json
import os
import re
import sys

series = os.environ["SERIES"]
try:
    releases = json.loads(os.environ.get("RELEASES_JSON", "[]"))
except json.JSONDecodeError:
    sys.exit(2)

if not isinstance(releases, list):
    sys.exit(2)

if series.count(".") == 0:
    pattern = re.compile(r"^" + re.escape(series) + r"\.[0-9]+\.[0-9]+$")
else:
    pattern = re.compile(r"^" + re.escape(series) + r"\.[0-9]+$")

def version_key(tag):
    return tuple(int(part) for part in tag[1:].split("."))

candidates = []
for release in releases:
    if not isinstance(release, dict):
        continue
    if release.get("draft") or release.get("prerelease"):
        continue
    tag = release.get("tag_name", "")
    if pattern.fullmatch(tag):
        candidates.append(tag)

if candidates:
    print(max(candidates, key=version_key))
PY
  )"; then
    error "Unable to parse GitHub releases while resolving floating ref '${series}'."
    exit 1
  fi

  if [[ -z "$tag" ]]; then
    error "Unable to resolve a stable Lopper release for floating ref '${series}'."
    exit 1
  fi
  printf '%s' "$tag"
}

resolve_requested_tag() {
  local requested
  requested="$(trim "${LOPPER_VERSION:-action}")"
  local requested_lower
  requested_lower="$(lower "$requested")"

  if [[ -z "$requested" || "$requested_lower" == "action" ]]; then
    local action_ref
    action_ref="$(trim "${LOPPER_ACTION_REF:-}")"
    if [[ -n "$action_ref" ]] && is_concrete_release_ref "$action_ref"; then
      printf '%s' "$action_ref"
      return
    fi
    if [[ -n "$action_ref" ]] && is_floating_release_ref "$action_ref"; then
      resolve_release_series_tag "$action_ref"
      return
    fi
    resolve_latest_tag
    return
  fi

  if [[ "$requested_lower" == "latest" ]]; then
    resolve_latest_tag
    return
  fi

  requested="$(normalize_explicit_version "$requested")"
  if is_floating_release_ref "$requested"; then
    resolve_release_series_tag "$requested"
    return
  fi
  printf '%s' "$requested"
}

detect_os() {
  local value
  value="$(trim "${LOPPER_ACTION_OS:-}")"
  if [[ -z "$value" ]]; then
    value="$(uname -s)"
  fi

  case "$(lower "$value")" in
    linux) printf 'linux' ;;
    darwin) printf 'darwin' ;;
    *)
      error "Unsupported runner OS '${value}'. Lopper release downloads are available for Linux and macOS runners."
      exit 1
      ;;
  esac
}

detect_arch() {
  local value
  value="$(trim "${LOPPER_ACTION_ARCH:-}")"
  if [[ -z "$value" ]]; then
    value="$(uname -m)"
  fi

  case "$(lower "$value")" in
    x86_64 | amd64) printf 'amd64' ;;
    arm64 | aarch64) printf 'arm64' ;;
    *)
      error "Unsupported runner architecture '${value}'. Lopper release downloads are available for amd64 and arm64."
      exit 1
      ;;
  esac
}

tag="$(resolve_requested_tag)"
validate_tag "$tag"

goos="$(detect_os)"
goarch="$(detect_arch)"
archive_name="lopper_${tag}_${goos}_${goarch}.tar.gz"
download_url="https://github.com/ben-ranford/lopper/releases/download/${tag}/${archive_name}"

write_output "resolved-version" "$tag"

if [[ "${LOPPER_INSTALL_DRY_RUN:-}" == "1" ]]; then
  write_output "download-url" "$download_url"
  printf 'resolved-version=%s\n' "$tag"
  printf 'download-url=%s\n' "$download_url"
  exit 0
fi

runner_temp="$(trim "${RUNNER_TEMP:-}")"
if [[ -z "$runner_temp" ]]; then
  runner_temp="${TMPDIR:-/tmp}"
fi
runner_temp="${runner_temp%/}"
install_dir="$(trim "${LOPPER_INSTALL_DIR:-}")"
if [[ -z "$install_dir" ]]; then
  install_dir="${runner_temp}/lopper-action/bin"
fi
mkdir -p "$install_dir"

work_dir="$(mktemp -d "${runner_temp}/lopper-action.XXXXXX")"
cleanup() {
  local status=$?
  rm -rf "$work_dir"
  return "$status"
}
trap cleanup EXIT INT TERM

archive_path="${work_dir}/${archive_name}"
curl_with_token -fsSL "$download_url" -o "$archive_path"
tar -xzf "$archive_path" -C "$work_dir"

binary_path="$(find "$work_dir" -type f -name lopper -print | head -n 1)"
if [[ -z "$binary_path" ]]; then
  error "Downloaded archive did not contain a lopper binary."
  exit 1
fi

installed_binary="${install_dir}/lopper"
cp "$binary_path" "$installed_binary"
chmod +x "$installed_binary"

if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$install_dir" >> "$GITHUB_PATH"
fi

lopper_version="$("$installed_binary" --version)"
write_output "lopper-version" "$lopper_version"
write_output "binary-path" "$installed_binary"
printf 'Installed %s at %s\n' "$lopper_version" "$installed_binary"

#!/usr/bin/env bash
set -euo pipefail

image_name="${IMAGE_NAME:-}"
image_tags="${IMAGE_TAGS:-}"
image_arch_suffix="${IMAGE_ARCH_SUFFIX:-}"
valid_image_tag_pattern='^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'

fail() {
  printf '::error::%s\n' "$1" >&2
  exit 1
}

trim_tag() {
  local tag="$1"
  tag="${tag%$'\r'}"
  tag="${tag#"${tag%%[![:space:]]*}"}"
  tag="${tag%"${tag##*[![:space:]]}"}"
  printf '%s' "$tag"
}

reject_malformed_tag() {
  local tag="$1"
  printf '::error::Malformed image tag rejected. Tags must match %s.\n' "$valid_image_tag_pattern" >&2
  printf 'Rejected image tag: %q\n' "$tag" >&2
  exit 1
}

if [ -z "$image_name" ]; then
  fail "IMAGE_NAME is required."
fi

declare -a sanitized_tags=()
while IFS= read -r raw_tag || [ -n "$raw_tag" ]; do
  tag="$(trim_tag "$raw_tag")"
  if [ -z "$tag" ]; then
    continue
  fi
  if [[ ! "$tag" =~ $valid_image_tag_pattern ]]; then
    reject_malformed_tag "$tag"
  fi
  sanitized_tags+=("$tag")
done <<< "$image_tags"

if [ "${#sanitized_tags[@]}" -eq 0 ]; then
  fail "No valid image tags were provided."
fi

for tag in "${sanitized_tags[@]}"; do
  printf '%s:%s%s\n' "$image_name" "$tag" "$image_arch_suffix"
done

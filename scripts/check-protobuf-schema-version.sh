#!/usr/bin/env bash

set -euo pipefail
export LC_ALL=C

readonly schema_path="api/proto"
readonly version_path="shared/protocol/version.json"

base_revision="${1:-${KENT_CI_BASE_REVISION:-}}"
if [ -z "$base_revision" ] && [ -n "${GITHUB_BASE_REF:-}" ]; then
	base_revision="refs/remotes/origin/${GITHUB_BASE_REF}"
	if ! git rev-parse --verify "${base_revision}^{commit}" >/dev/null 2>&1; then
		git fetch --no-tags origin \
			"refs/heads/${GITHUB_BASE_REF}:${base_revision}"
	fi
elif [ -z "$base_revision" ] && [ -n "${GITHUB_EVENT_BEFORE:-}" ]; then
	case "$GITHUB_EVENT_BEFORE" in
	0000000000000000000000000000000000000000)
		;;
	*)
		base_revision="$GITHUB_EVENT_BEFORE"
		if ! git rev-parse --verify "${base_revision}^{commit}" >/dev/null 2>&1; then
			git fetch --no-tags origin "$base_revision"
		fi
		;;
	esac
fi
if [ -z "$base_revision" ] && git rev-parse --verify HEAD^ >/dev/null 2>&1; then
	base_revision="HEAD^"
fi
if [ -z "$base_revision" ]; then
	echo "unable to determine the base revision for the Protobuf schema/version check" >&2
	exit 2
fi
if ! git rev-parse --verify "${base_revision}^{commit}" >/dev/null 2>&1; then
	echo "invalid base revision for the Protobuf schema/version check: ${base_revision}" >&2
	exit 2
fi

schema_changed=false
if ! git diff --quiet "$base_revision" -- "$schema_path"; then
	schema_changed=true
elif [ -n "$(git ls-files --others --exclude-standard -- "$schema_path")" ]; then
	schema_changed=true
fi

if [ "$schema_changed" = false ]; then
	exit 0
fi

parse_version() {
	local source_name="$1"
	local source_path="$2"
	local version

	if ! version="$(jq -er '
		if type == "object" and
			(.version | type) == "string"
		then .version
		else error("expected {\"version\":\"<canonical numeric value>\"}")
		end
	' "$source_path" 2>/dev/null)"; then
		echo "${source_name} ${version_path} is malformed" >&2
		return 1
	fi
	case "$version" in
	"" | *[!0-9]*)
		echo "${source_name} ${version_path} version must be numeric" >&2
		return 1
		;;
	0 | [1-9]*)
		;;
	*)
		echo "${source_name} ${version_path} version is not canonical: ${version}" >&2
		return 1
		;;
	esac
	echo "$version"
}

base_version="$(
	git show "${base_revision}:${version_path}" |
		parse_version "base" -
)" || exit 1
current_version="$(parse_version "current" "$version_path")" || exit 1

version_increased=false
if [ "${#current_version}" -gt "${#base_version}" ]; then
	version_increased=true
elif [ "${#current_version}" -eq "${#base_version}" ] &&
	[[ "$current_version" > "$base_version" ]]; then
	version_increased=true
fi
if [ "$version_increased" = false ]; then
	echo "changes under ${schema_path}/ require ${version_path} to increase from ${base_version}; current value is ${current_version}" >&2
	exit 1
fi

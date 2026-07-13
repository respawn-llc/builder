#!/usr/bin/env bash

kent_sha256sum_command="sha256sum"
kent_shasum_command="shasum"

kent_select_sha256_command() {
	local purpose="$1"
	local preferred="${2:-}"
	local choices=()

	if [[ -n "$preferred" ]]; then
		case "$preferred" in
		"$kent_sha256sum_command" | "$kent_shasum_command")
			choices=("$preferred")
			;;
		*)
			printf 'Unsupported SHA-256 command %s; accepted commands: %s %s\n' \
				"$preferred" "$kent_sha256sum_command" "$kent_shasum_command" >&2
			return 2
			;;
		esac
	elif [[ "$(uname -s)" == "Darwin" ]]; then
		choices=("$kent_shasum_command" "$kent_sha256sum_command")
	else
		choices=("$kent_sha256sum_command" "$kent_shasum_command")
	fi

	local command_name
	for command_name in "${choices[@]}"; do
		if command -v "$command_name" >/dev/null 2>&1; then
			printf '%s\n' "$command_name"
			return
		fi
	done

	printf 'One of the following commands is required to %s: %s\n' \
		"$purpose" "${choices[*]}" >&2
	return 2
}

kent_sha256_file() {
	local command_name="$1"
	local file_path="$2"

	case "$command_name" in
	"$kent_sha256sum_command")
		"$command_name" "$file_path" | awk '{print $1}'
		;;
	"$kent_shasum_command")
		"$command_name" -a 256 "$file_path" | awk '{print $1}'
		;;
	*)
		printf 'Unsupported SHA-256 command %s; accepted commands: %s %s\n' \
			"$command_name" "$kent_sha256sum_command" "$kent_shasum_command" >&2
		return 2
		;;
	esac
}

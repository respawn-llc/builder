kent_sha256sum_command="sha256sum"
kent_shasum_command="shasum"

kent_select_sha256_command() (
	kent_checksum_purpose="$1"
	kent_checksum_preferred="${2:-}"

	if [ -n "$kent_checksum_preferred" ]; then
		case "$kent_checksum_preferred" in
		"$kent_sha256sum_command" | "$kent_shasum_command")
			kent_checksum_first="$kent_checksum_preferred"
			kent_checksum_second=""
			;;
		*)
			printf 'Unsupported SHA-256 command %s; accepted commands: %s %s\n' \
				"$kent_checksum_preferred" "$kent_sha256sum_command" "$kent_shasum_command" >&2
			return 2
			;;
		esac
	elif [ "$(uname -s)" = "Darwin" ]; then
		kent_checksum_first="$kent_shasum_command"
		kent_checksum_second="$kent_sha256sum_command"
	else
		kent_checksum_first="$kent_sha256sum_command"
		kent_checksum_second="$kent_shasum_command"
	fi

	for kent_checksum_candidate in "$kent_checksum_first" "$kent_checksum_second"; do
		if [ -n "$kent_checksum_candidate" ] && command -v "$kent_checksum_candidate" >/dev/null 2>&1; then
			printf '%s\n' "$kent_checksum_candidate"
			return
		fi
	done

	printf 'One of the following commands is required to %s: %s %s\n' \
		"$kent_checksum_purpose" "$kent_sha256sum_command" "$kent_shasum_command" >&2
	return 2
)

kent_sha256_file() (
	kent_checksum_command="$1"
	kent_checksum_file="$2"

	case "$kent_checksum_command" in
	"$kent_sha256sum_command")
		"$kent_checksum_command" "$kent_checksum_file" | awk '{print $1}'
		;;
	"$kent_shasum_command")
		"$kent_checksum_command" -a 256 "$kent_checksum_file" | awk '{print $1}'
		;;
	*)
		printf 'Unsupported SHA-256 command %s; accepted commands: %s %s\n' \
			"$kent_checksum_command" "$kent_sha256sum_command" "$kent_shasum_command" >&2
		return 2
		;;
	esac
)

kent_verify_sha256_manifest() (
	kent_checksum_command="$1"
	kent_checksum_manifest="$2"

	case "$kent_checksum_command" in
	"$kent_sha256sum_command")
		"$kent_checksum_command" -c "$kent_checksum_manifest"
		;;
	"$kent_shasum_command")
		"$kent_checksum_command" -a 256 -c "$kent_checksum_manifest"
		;;
	*)
		printf 'Unsupported SHA-256 command %s; accepted commands: %s %s\n' \
			"$kent_checksum_command" "$kent_sha256sum_command" "$kent_shasum_command" >&2
		return 2
		;;
	esac
)

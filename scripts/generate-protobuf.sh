#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly repository_root
readonly go_generated_path="shared/protoapi/gen"
readonly typescript_generated_path="apps/desktop/packages/server-api-contract/src/gen"

usage() {
	echo "usage: scripts/generate-protobuf.sh <generate|check>" >&2
}

if [[ $# -ne 1 ]]; then
	usage
	exit 2
fi

case "$1" in
generate | check)
	readonly mode="$1"
	;;
*)
	usage
	exit 2
	;;
esac

staging_root="$(mktemp -d "${repository_root}/.protobuf-generation.XXXXXX")"
readonly staging_root

replacement_in_progress=false
go_destination=
typescript_destination=
go_backup=
typescript_backup=
go_had_destination=false
typescript_had_destination=false
go_replaced=false
typescript_replaced=false

rollback_replacement() {
	local status=0

	if [[ "${typescript_replaced}" == true && -e "${typescript_destination}" ]]; then
		find "${typescript_destination}" -depth -delete || status=1
	fi
	if [[ "${go_replaced}" == true && -e "${go_destination}" ]]; then
		find "${go_destination}" -depth -delete || status=1
	fi
	if [[ "${typescript_had_destination}" == true && -e "${typescript_backup}" ]]; then
		mv "${typescript_backup}" "${typescript_destination}" || status=1
	fi
	if [[ "${go_had_destination}" == true && -e "${go_backup}" ]]; then
		mv "${go_backup}" "${go_destination}" || status=1
	fi

	return "${status}"
}

cleanup() {
	local exit_status=$?
	trap - EXIT INT TERM

	if [[ "${replacement_in_progress}" == true ]]; then
		if ! rollback_replacement; then
			echo "failed to restore generated output after interrupted replacement" >&2
			exit_status=1
		fi
	fi
	if [[ -e "${staging_root}" ]]; then
		find "${staging_root}" -depth -delete || exit_status=1
	fi

	exit "${exit_status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

(
	cd "${repository_root}/tools/protobuf"
	go tool buf generate "${repository_root}" \
		--config "${repository_root}/buf.yaml" \
		--template "${repository_root}/buf.gen.yaml" \
		--output "${staging_root}"
)

normalize_generated_typescript_endings() {
	local generated_file
	local trailing_bytes

	while IFS= read -r -d '' generated_file; do
		while [[ "$(wc -c <"${generated_file}")" -ge 2 ]]; do
			trailing_bytes="$(tail -c 2 "${generated_file}" | od -An -tx1 | tr -d '[:space:]')"
			if [[ "${trailing_bytes}" != "0a0a" ]]; then
				break
			fi
			truncate -s -1 "${generated_file}"
		done
	done < <(find "${staging_root}/${typescript_generated_path}" -type f -name '*.ts' -print0)
}

normalize_generated_typescript_endings

compare_generated_tree() {
	local relative_path="$1"
	local generated_path="${staging_root}/${relative_path}"
	local checked_in_path="${repository_root}/${relative_path}"

	if [[ ! -d "${generated_path}" ]]; then
		echo "Protobuf generation did not produce ${relative_path}" >&2
		return 1
	fi
	if [[ ! -d "${checked_in_path}" ]]; then
		echo "generated output differs: ${relative_path} is missing" >&2
		return 1
	fi
	if ! diff -qr "${checked_in_path}" "${generated_path}" >&2; then
		echo "generated output differs: ${relative_path}" >&2
		return 1
	fi
}

replace_generated_trees() {
	go_destination="${repository_root}/${go_generated_path}"
	typescript_destination="${repository_root}/${typescript_generated_path}"
	go_backup="${staging_root}/previous-go"
	typescript_backup="${staging_root}/previous-typescript"

	mkdir -p "$(dirname "${go_destination}")" "$(dirname "${typescript_destination}")"
	replacement_in_progress=true

	if [[ -e "${go_destination}" ]]; then
		mv "${go_destination}" "${go_backup}"
		go_had_destination=true
	fi
	if [[ -e "${typescript_destination}" ]]; then
		mv "${typescript_destination}" "${typescript_backup}"
		typescript_had_destination=true
	fi

	mv "${staging_root}/${go_generated_path}" "${go_destination}"
	go_replaced=true

	mv "${staging_root}/${typescript_generated_path}" "${typescript_destination}"
	typescript_replaced=true
	replacement_in_progress=false
}

case "${mode}" in
generate)
	replace_generated_trees
	;;
check)
	status=0
	compare_generated_tree "${go_generated_path}" || status=1
	compare_generated_tree "${typescript_generated_path}" || status=1
	exit "${status}"
	;;
esac

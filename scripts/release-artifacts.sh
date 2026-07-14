#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
	cat <<'USAGE'
Usage:
  scripts/release-artifacts.sh build --version X.Y.Z [--dist-dir dist]
  scripts/release-artifacts.sh verify-manifest [--dist-dir dist]
  scripts/release-artifacts.sh verify-linux-static --version X.Y.Z [--dist-dir dist]
  scripts/release-artifacts.sh smoke-test --version X.Y.Z --goos <os> --goarch <arch> --archive-ext <ext> [--binary-ext <ext>] [--dist-dir dist]
  scripts/release-artifacts.sh smoke-windows-installer --version X.Y.Z --goarch <arch> [--dist-dir dist]
USAGE
}

resolve_path() {
	local path="$1"
	case "$path" in
	/*) printf '%s\n' "$path" ;;
	*) printf '%s/%s\n' "$repo_root" "$path" ;;
	esac
}

require_value() {
	local name="$1"
	local value="$2"
	if [ -n "$value" ]; then
		return
	fi
	echo "Missing required argument: ${name}" >&2
	usage >&2
	exit 1
}

require_option_arg() {
	local opt="$1"
	local next="${2-}"
	if [[ -n "$next" && "$next" != --* ]]; then
		return
	fi
	echo "Missing required argument value: ${opt}" >&2
	usage >&2
	exit 1
}

clean_dist_release_artifacts() {
	local dist_path="$1"
	find "$dist_path" -maxdepth 1 -type f \( -name 'kent_*.tar.gz' -o -name 'kent_*.zip' -o -name 'checksums.txt' \) -delete
}

release_targets() {
	cat <<'EOF'
darwin arm64
linux amd64
linux arm64
windows amd64
windows arm64
EOF
}

build_archives() {
	require_value "--version" "$version"

	local dist_path
	dist_path="$(resolve_path "$dist_dir")"
	mkdir -p "$dist_path"
	clean_dist_release_artifacts "$dist_path"

	local staging_dir
	staging_dir="$(mktemp -d)"

	local build_os build_arch ext archive_ext out frontend_build_done
	frontend_build_done=0
	while read -r build_os build_arch; do
		if [ "$build_os" = "windows" ]; then
			ext=".exe"
			archive_ext="zip"
		else
			ext=""
			archive_ext="tar.gz"
		fi

		out="kent_${version}_${build_os}_${build_arch}"
		if [ "$frontend_build_done" -eq 0 ]; then
			env GOOS="$build_os" GOARCH="$build_arch" KENT_VERSION="$version" \
				bash scripts/build.sh desktop tui server --output "$staging_dir/${out}${ext}"
			frontend_build_done=1
		else
			env GOOS="$build_os" GOARCH="$build_arch" KENT_VERSION="$version" KENT_SKIP_FRONTEND=1 \
				bash scripts/build.sh server --output "$staging_dir/${out}${ext}"
		fi

		if [ "$archive_ext" = "zip" ]; then
			(
				cd "$staging_dir"
				zip -q "$dist_path/${out}.zip" "${out}${ext}"
			)
		else
			(
				cd "$staging_dir"
				tar -czf "$dist_path/${out}.tar.gz" "${out}${ext}"
			)
		fi
	done < <(release_targets)

	(
		cd "$dist_path"
		shasum -a 256 "kent_${version}_"*.tar.gz "kent_${version}_"*.zip >checksums.txt
	)
}

verify_manifest() {
	local dist_path
	dist_path="$(resolve_path "$dist_dir")"
	(
		cd "$dist_path"
		shasum -a 256 -c checksums.txt
	)
}

verify_linux_static() {
	require_value "--version" "$version"

	local dist_path
	dist_path="$(resolve_path "$dist_dir")"

	local staging_dir
	staging_dir="$(mktemp -d)"

	local build_arch archive_path binary_path file_output
	for build_arch in amd64 arm64; do
		archive_path="$dist_path/kent_${version}_linux_${build_arch}.tar.gz"
		tar -xzf "$archive_path" -C "$staging_dir"

		binary_path="$staging_dir/kent_${version}_linux_${build_arch}"
		file_output="$(file "$binary_path")"
		echo "$file_output"

		case "$file_output" in
		*"dynamically linked"*)
			echo "Dynamic linking is not allowed for Linux release binaries." >&2
			exit 1
			;;
		*"statically linked"*) ;;
		*)
			echo "Unable to confirm static linking for ${binary_path}." >&2
			exit 1
			;;
		esac
	done
}

smoke_test() {
	require_value "--version" "$version"
	require_value "--goos" "$goos"
	require_value "--goarch" "$goarch"
	require_value "--archive-ext" "$archive_ext"
	if [ "$goos" = "windows" ]; then
		require_value "--binary-ext" "$binary_ext"
	fi

	local dist_path
	dist_path="$(resolve_path "$dist_dir")"

	local asset_base archive_path smoke_dir binary_path
	asset_base="kent_${version}_${goos}_${goarch}"
	archive_path="$dist_path/${asset_base}.${archive_ext}"
	smoke_dir="$(mktemp -d)"

	case "$archive_ext" in
	tar.gz)
		tar -xzf "$archive_path" -C "$smoke_dir"
		;;
	zip)
		unzip -q "$archive_path" -d "$smoke_dir"
		;;
	*)
		echo "Unsupported archive extension: ${archive_ext}" >&2
		exit 1
		;;
	esac

	binary_path="$smoke_dir/${asset_base}${binary_ext}"
	local version_output expected_version
	version_output="$("$binary_path" --version)"
	expected_version="${version#v}"
	if [ "$version_output" != "$expected_version" ]; then
		echo "unexpected version output for ${binary_path}: got ${version_output}, want ${expected_version}" >&2
		exit 1
	fi
	"$binary_path" --help >/dev/null
}

to_powershell_path() {
	local value="$1"
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -w "$value"
		return
	fi
	printf '%s\n' "$value"
}

smoke_windows_installer() {
	require_value "--version" "$version"
	require_value "--goarch" "$goarch"
	if ! command -v powershell >/dev/null 2>&1; then
		echo "powershell is required for Windows installer smoke test" >&2
		exit 1
	fi

	local dist_path normalized tag asset archive_path smoke_dir release_base release_dir install_dir install_script
	dist_path="$(resolve_path "$dist_dir")"
	normalized="${version#v}"
	tag="v${normalized}"
	asset="kent_${normalized}_windows_${goarch}.zip"
	archive_path="$dist_path/$asset"
	if [ ! -f "$archive_path" ]; then
		echo "missing Windows release archive: $archive_path" >&2
		exit 1
	fi
	if [ ! -f "$dist_path/checksums.txt" ]; then
		echo "missing release checksum manifest: $dist_path/checksums.txt" >&2
		exit 1
	fi

	smoke_dir="$(mktemp -d)"
	release_base="$smoke_dir/releases"
	release_dir="$release_base/$tag"
	install_dir="$smoke_dir/install"
	mkdir -p "$release_dir" "$install_dir"
	cp "$archive_path" "$release_dir/$asset"
	cp "$dist_path/checksums.txt" "$release_dir/checksums.txt"
	install_script="$(to_powershell_path "$repo_root/scripts/install.ps1")"

	KENT_RELEASE_BASE="$(to_powershell_path "$release_base")" \
		powershell -NoProfile -ExecutionPolicy Bypass -File "$install_script" \
		-Version "$normalized" \
		-InstallDir "$(to_powershell_path "$install_dir")" \
		-Yes \
		-NoPath \
		-NoDeps \
		-NoServiceRestart

	powershell -NoProfile -ExecutionPolicy Bypass -File "$install_script" \
		-Uninstall \
		-InstallDir "$(to_powershell_path "$install_dir")" \
		-Yes
}

mode="${1:-}"
if [ -z "$mode" ]; then
	usage >&2
	exit 1
fi
shift

dist_dir="dist"
version=""
goos=""
goarch=""
archive_ext=""
binary_ext=""

while [[ $# -gt 0 ]]; do
	case "$1" in
	--dist-dir)
		require_option_arg "$1" "${2-}"
		dist_dir="${2:-}"
		shift 2
		;;
	--version)
		require_option_arg "$1" "${2-}"
		version="${2:-}"
		shift 2
		;;
	--goos)
		require_option_arg "$1" "${2-}"
		goos="${2:-}"
		shift 2
		;;
	--goarch)
		require_option_arg "$1" "${2-}"
		goarch="${2:-}"
		shift 2
		;;
	--archive-ext)
		require_option_arg "$1" "${2-}"
		archive_ext="${2:-}"
		shift 2
		;;
	--binary-ext)
		require_option_arg "$1" "${2-}"
		binary_ext="${2:-}"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "Unknown argument: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

version="${version#v}"

case "$mode" in
build)
	build_archives
	;;
verify-manifest)
	verify_manifest
	;;
verify-linux-static)
	verify_linux_static
	;;
smoke-test)
	smoke_test
	;;
smoke-windows-installer)
	smoke_windows_installer
	;;
*)
	echo "Unknown mode: $mode" >&2
	usage >&2
	exit 1
	;;
esac

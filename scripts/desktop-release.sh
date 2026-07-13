#!/usr/bin/env bash
set -euo pipefail

# Desktop (Tauri) release packaging. Sibling to release-artifacts.sh, kept separate
# because the desktop bundles, updater signatures, and latest.json have a different
# shape from the CLI archives. Workflow YAML stays orchestration-only; the naming,
# updater latest.json, and checksums live here so they are testable.
#
# Subcommands:
#   build    : build the host platform's bundles and stage them into --dist-dir with
#              stable, self-describing names (+ the macOS/Linux updater artifacts).
#   assemble : from a --dist-dir holding all platforms' staged artifacts, emit
#              latest.json (the Tauri updater manifest) and desktop-checksums.txt.
#   self-test: assemble against built-in fixtures and assert the manifest shape.

usage() {
	cat <<'USAGE'
Usage:
  scripts/desktop-release.sh build    [--version X.Y.Z] [--dist-dir dist/desktop] [--skip-install]
  scripts/desktop-release.sh assemble [--version X.Y.Z] [--dist-dir dist/desktop] [--base-url URL] [--pub-date RFC3339] [--notes TEXT]
  scripts/desktop-release.sh self-test

Stable staged names (host-platform bundles):
  Kent_<ver>_aarch64.dmg                   macOS installer
  Kent_<ver>_aarch64.app.tar.gz(.sig)      macOS updater artifact
  Kent_<ver>_amd64.AppImage(.sig)          Linux updater artifact
  Kent_<ver>_amd64.deb                     Linux package
  Kent_<ver>_x64-setup.exe(.sig)           Windows NSIS installer + updater artifact

Defaults:
  --dist-dir : dist/desktop
  --version  : KENT_VERSION or the VERSION file
  --base-url : https://github.com/respawn-llc/kent/releases/download/v<ver>
USAGE
}

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repo_root/scripts/lib/checksum.sh"

desktop_checksum_command=""

require_value() {
	local flag="$1" value="${2:-}"
	if [[ -z "$value" || "$value" == --* ]]; then
		echo "${flag} requires a value" >&2
		exit 1
	fi
}

resolve_version() {
	local version="${KENT_VERSION:-}"
	if [[ -z "$version" && -f "$repo_root/VERSION" ]]; then
		version="$(tr -d '[:space:]' <"$repo_root/VERSION")"
	fi
	printf '%s' "${version#v}"
}

# stage_artifact <src> <dist_dir>/<dest-name>: copy if the source exists, else fail
# loudly (a missing expected bundle is a build break, not something to skip).
stage_required() {
	local src="$1" dest="$2"
	if [[ ! -f "$src" ]]; then
		echo "expected bundle not found: $src" >&2
		exit 1
	fi
	cp "$src" "$dest"
}

cmd_build() {
	local version="" dist_dir="" skip_install=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--version) require_value "$1" "${2:-}"; version="$2"; shift 2 ;;
		--dist-dir) require_value "$1" "${2:-}"; dist_dir="$2"; shift 2 ;;
		--skip-install) skip_install="--skip-install"; shift ;;
		*) echo "Unknown arg: $1" >&2; usage >&2; exit 1 ;;
		esac
	done
	[[ -n "$version" ]] || version="$(resolve_version)"
	version="${version#v}"
	[[ -n "$version" ]] || { echo "Unable to resolve version" >&2; exit 1; }
	[[ -n "$dist_dir" ]] || dist_dir="dist/desktop"
	mkdir -p "$dist_dir"
	local dist_abs; dist_abs="$(cd "$dist_dir" && pwd)"

	bash "$repo_root/scripts/build-desktop.sh" --version "$version" --require-updater-key ${skip_install:+$skip_install}

	local bundle="$repo_root/apps/desktop/src-tauri/target/release/bundle"
	case "$(uname -s)" in
	Darwin)
		stage_required "$bundle/dmg/Kent_${version}_aarch64.dmg" "$dist_abs/Kent_${version}_aarch64.dmg"
		# Tauri names the macOS updater artifact generically (Kent.app.tar.gz); rename
		# to a versioned, self-describing release asset that latest.json points at.
		stage_required "$bundle/macos/Kent.app.tar.gz" "$dist_abs/Kent_${version}_aarch64.app.tar.gz"
		stage_required "$bundle/macos/Kent.app.tar.gz.sig" "$dist_abs/Kent_${version}_aarch64.app.tar.gz.sig"
		;;
	Linux)
		stage_required "$bundle/appimage/Kent_${version}_amd64.AppImage" "$dist_abs/Kent_${version}_amd64.AppImage"
		stage_required "$bundle/appimage/Kent_${version}_amd64.AppImage.sig" "$dist_abs/Kent_${version}_amd64.AppImage.sig"
		stage_required "$bundle/deb/Kent_${version}_amd64.deb" "$dist_abs/Kent_${version}_amd64.deb"
		;;
	MINGW* | MSYS* | CYGWIN*)
		stage_required "$bundle/nsis/Kent_${version}_x64-setup.exe" "$dist_abs/Kent_${version}_x64-setup.exe"
		stage_required "$bundle/nsis/Kent_${version}_x64-setup.exe.sig" "$dist_abs/Kent_${version}_x64-setup.exe.sig"
		;;
	*)
		echo "Unsupported host platform for desktop build: $(uname -s)" >&2
		exit 1
		;;
	esac
	echo "Staged desktop bundles for ${version} into ${dist_dir}" >&2
	ls -1 "$dist_abs" >&2
}

# emit_latest_json <dist_dir> <version> <base_url> <pub_date> <notes>: build the
# Tauri updater manifest from whichever platform updater artifacts are present.
emit_latest_json() {
	local dist_dir="$1" version="$2" base_url="$3" pub_date="$4" notes="$5"
	local platforms="{}"
	local mac_tar="$dist_dir/Kent_${version}_aarch64.app.tar.gz"
	local linux_img="$dist_dir/Kent_${version}_amd64.AppImage"
	local win_exe="$dist_dir/Kent_${version}_x64-setup.exe"
	if [[ -f "$mac_tar.sig" ]]; then
		platforms="$(jq -n --argjson p "$platforms" \
			--arg sig "$(cat "$mac_tar.sig")" \
			--arg url "${base_url}/Kent_${version}_aarch64.app.tar.gz" \
			'$p + {"darwin-aarch64": {signature: $sig, url: $url}}')"
	fi
	if [[ -f "$linux_img.sig" ]]; then
		platforms="$(jq -n --argjson p "$platforms" \
			--arg sig "$(cat "$linux_img.sig")" \
			--arg url "${base_url}/Kent_${version}_amd64.AppImage" \
			'$p + {"linux-x86_64": {signature: $sig, url: $url}}')"
	fi
	if [[ -f "$win_exe.sig" ]]; then
		platforms="$(jq -n --argjson p "$platforms" \
			--arg sig "$(cat "$win_exe.sig")" \
			--arg url "${base_url}/Kent_${version}_x64-setup.exe" \
			'$p + {"windows-x86_64": {signature: $sig, url: $url}}')"
	fi
	if [[ "$platforms" == "{}" ]]; then
		echo "no updater artifacts (.sig) found in $dist_dir" >&2
		exit 1
	fi
	jq -n \
		--arg version "$version" \
		--arg notes "$notes" \
		--arg pub_date "$pub_date" \
		--argjson platforms "$platforms" \
		'{version: $version, notes: $notes, pub_date: $pub_date, platforms: $platforms}'
}

cmd_assemble() {
	local version="" dist_dir="" base_url="" pub_date="" notes="See the release notes."
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--version) require_value "$1" "${2:-}"; version="$2"; shift 2 ;;
		--dist-dir) require_value "$1" "${2:-}"; dist_dir="$2"; shift 2 ;;
		--base-url) require_value "$1" "${2:-}"; base_url="$2"; shift 2 ;;
		--pub-date) require_value "$1" "${2:-}"; pub_date="$2"; shift 2 ;;
		--notes) require_value "$1" "${2:-}"; notes="$2"; shift 2 ;;
		*) echo "Unknown arg: $1" >&2; usage >&2; exit 1 ;;
		esac
	done
	[[ -n "$version" ]] || version="$(resolve_version)"
	version="${version#v}"
	[[ -n "$version" ]] || { echo "Unable to resolve version" >&2; exit 1; }
	[[ -n "$dist_dir" ]] || dist_dir="dist/desktop"
	[[ -d "$dist_dir" ]] || { echo "dist dir not found: $dist_dir" >&2; exit 1; }
	[[ -n "$base_url" ]] || base_url="https://github.com/respawn-llc/kent/releases/download/v${version}"
	[[ -n "$pub_date" ]] || pub_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	desktop_checksum_command="$(
		kent_select_sha256_command \
			"generate desktop release checksums" \
			"${KENT_DESKTOP_CHECKSUM_COMMAND:-}"
	)"

	emit_latest_json "$dist_dir" "$version" "$base_url" "$pub_date" "$notes" >"$dist_dir/latest.json"

	# Desktop checksum manifest over the distributable bundles (not the .sig/json).
	# Portable globbing (BSD/macOS find lacks -printf); guard each pattern in case a
	# platform's bundles are absent from this dist dir.
	(
		cd "$dist_dir"
		for f in *.dmg *.AppImage *.deb *.app.tar.gz *-setup.exe; do
			[[ -f "$f" ]] || continue
			printf '%s  %s\n' "$(kent_sha256_file "$desktop_checksum_command" "$f")" "$f"
		done | sort -k2 >desktop-checksums.txt
	)

	echo "Wrote ${dist_dir}/latest.json and ${dist_dir}/desktop-checksums.txt" >&2
	cat "$dist_dir/latest.json" >&2
}

cmd_self_test() {
	local tmp; tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' RETURN
	local v="9.9.9"
	printf 'MAC_SIG_CONTENT' >"$tmp/Kent_${v}_aarch64.app.tar.gz.sig"
	printf 'fake-mac-tar' >"$tmp/Kent_${v}_aarch64.app.tar.gz"
	printf 'LINUX_SIG_CONTENT' >"$tmp/Kent_${v}_amd64.AppImage.sig"
	printf 'fake-appimage' >"$tmp/Kent_${v}_amd64.AppImage"
	printf 'WIN_SIG_CONTENT' >"$tmp/Kent_${v}_x64-setup.exe.sig"
	printf 'fake-setup-exe' >"$tmp/Kent_${v}_x64-setup.exe"

	cmd_assemble --version "$v" --dist-dir "$tmp" --pub-date "2026-01-02T03:04:05Z" --base-url "https://example.test/v$v" >/dev/null

	local json="$tmp/latest.json"
	local fail=0
	assert_eq() {
		local got="$1" want="$2" label="$3"
		if [[ "$got" != "$want" ]]; then
			echo "FAIL ${label}: got [$got] want [$want]" >&2
			fail=1
		fi
	}
	assert_eq "$(jq -r '.version' "$json")" "$v" "version"
	assert_eq "$(jq -r '.pub_date' "$json")" "2026-01-02T03:04:05Z" "pub_date"
	assert_eq "$(jq -r '.platforms["darwin-aarch64"].signature' "$json")" "MAC_SIG_CONTENT" "mac signature"
	assert_eq "$(jq -r '.platforms["darwin-aarch64"].url' "$json")" "https://example.test/v$v/Kent_${v}_aarch64.app.tar.gz" "mac url"
	assert_eq "$(jq -r '.platforms["linux-x86_64"].signature' "$json")" "LINUX_SIG_CONTENT" "linux signature"
	assert_eq "$(jq -r '.platforms["linux-x86_64"].url' "$json")" "https://example.test/v$v/Kent_${v}_amd64.AppImage" "linux url"
	assert_eq "$(jq -r '.platforms["windows-x86_64"].signature' "$json")" "WIN_SIG_CONTENT" "windows signature"
	assert_eq "$(jq -r '.platforms["windows-x86_64"].url' "$json")" "https://example.test/v$v/Kent_${v}_x64-setup.exe" "windows url"
	# checksums.txt covers the distributable bundles, not the .sig/latest.json.
	assert_eq "$(wc -l <"$tmp/desktop-checksums.txt" | tr -d ' ')" "3" "checksum line count"

	if [[ "$fail" != "0" ]]; then
		echo "desktop-release self-test FAILED" >&2
		exit 1
	fi
	echo "desktop-release self-test OK" >&2
}

cmd="${1:-}"
shift || true
case "$cmd" in
build) cmd_build "$@" ;;
assemble) cmd_assemble "$@" ;;
self-test) cmd_self_test ;;
-h | --help | "") usage ;;
*) echo "Unknown subcommand: $cmd" >&2; usage >&2; exit 1 ;;
esac

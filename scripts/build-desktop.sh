#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
	cat <<'USAGE'
Usage: scripts/build-desktop.sh [--version vX.Y.Z|X.Y.Z] [--skip-install] [--install] [--require-updater-key] [-- <tauri build args>]

Builds the Kent desktop (Tauri) app bundle. The bundle version is stamped from
VERSION (or KENT_VERSION / --version) at build time via a `tauri build --config`
merge, so the committed manifests (tauri.conf.json, package.json, Cargo.toml)
stay at their 0.0.0 placeholder and are never hand-edited per release.

Options:
  --version       Override the bundle version. Defaults to KENT_VERSION or VERSION.
  --skip-install  Skip the workspace dependency install step.
  --install       macOS only: replace /Applications/Kent.app with the built app.
                  The previous installation is moved to the user's Trash.
  --require-updater-key
                  Fail when updater artifact signing is unavailable. Release
                  packaging uses this; local bundle QA disables updater artifacts
                  instead of requiring a private key.
  -h, --help      Show this help.

Arguments after `--` are forwarded to `tauri build`, e.g.:
  scripts/build-desktop.sh -- --bundles dmg --target aarch64-apple-darwin
USAGE
}

read_version() {
	local version="${KENT_VERSION:-}"
	if [ -z "$version" ] && [ -f VERSION ]; then
		version="$(tr -d '[:space:]' <VERSION)"
	fi
	printf '%s' "${version#v}"
}

tauri_args_select_bundles() {
	for arg in "$@"; do
		case "$arg" in
		--bundles | -b | --bundles=*)
			return 0
			;;
		esac
	done
	return 1
}

# Compile the Icon Composer .icon (the macOS 26 liquid-glass app icon source)
# into an Assets.car that bundle.icon references directly. We do this instead of
# letting the Tauri bundler invoke actool itself because its in-bundler actool
# call crashes on .icon inputs (tauri-apps/tauri#15315); the bundler copies a
# pre-built .car verbatim and still reads CFBundleIconName from it. macOS-only;
# Linux bundles use the PNG icons and ignore the .car.
compile_app_icon() {
	[ "$(uname -s)" = "Darwin" ] || return 0

	local icon_dir="apps/desktop/src-tauri/icons/Kent.icon"
	local out_car="apps/desktop/src-tauri/icons/Assets.car"
	[ -d "$icon_dir" ] || return 0

	if ! command -v actool >/dev/null 2>&1; then
		rm -f "$out_car"
		echo "actool not found; skipping liquid-glass app icon (Xcode 26+ required). Tauri will fall back to PNG -> icns." >&2
		return 0
	fi

	local tmp attempt out
	tmp="$(mktemp -d)"
	cp -R "$icon_dir" "$tmp/Icon.icon"

	for attempt in 1 2 3; do
		# actool talks to the ibtoold asset-catalog daemon, which wedges after the
		# first glass-icon compile and crashes subsequent ones; forcing a fresh
		# daemon per attempt makes the compile reliable (tauri-apps/tauri#15315).
		killall -9 ibtoold >/dev/null 2>&1 || true
		out="$tmp/out_${attempt}"
		mkdir -p "$out"
		if actool "$tmp/Icon.icon" \
			--compile "$out" \
			--output-format human-readable-text --notices --warnings \
			--output-partial-info-plist "$out/assetcatalog_generated_info.plist" \
			--app-icon Icon --include-all-app-icons \
			--accent-color AccentColor \
			--enable-on-demand-resources NO \
			--development-region en \
			--target-device mac \
			--minimum-deployment-target 15.0 \
			--platform macosx >/dev/null 2>&1 && [ -f "$out/Assets.car" ]; then
			cp "$out/Assets.car" "$out_car"
			echo "Compiled liquid-glass app icon -> ${out_car}" >&2
			return 0
		fi
	done

	echo "Failed to compile ${icon_dir} into Assets.car after 3 attempts." >&2
	return 1
}

install_macos_app() {
	local app_bundle="$1"
	local destination="/Applications/Kent.app"
	local trash_root=""
	local previous_dir=""
	local failed_dir=""
	local install_status=0

	if [ ! -d "$app_bundle" ]; then
		echo "Built macOS app bundle not found: ${app_bundle}" >&2
		return 1
	fi
	if [ -z "${HOME:-}" ]; then
		echo "HOME is required to preserve the previous Kent.app installation." >&2
		return 1
	fi
	trash_root="${HOME}/.Trash"
	mkdir -p "$trash_root"

	if [ -e "$destination" ] || [ -L "$destination" ]; then
		previous_dir="$(mktemp -d "$trash_root/Kent.app.previous.XXXXXX")"
		if ! mv "$destination" "$previous_dir/Kent.app"; then
			rmdir "$previous_dir" 2>/dev/null || true
			echo "Failed to move the existing ${destination} to Trash." >&2
			return 1
		fi
	fi

	if ditto "$app_bundle" "$destination"; then
		echo "Installed Kent desktop app -> ${destination}" >&2
		return 0
	else
		install_status=$?
	fi

	echo "Failed to install Kent desktop app at ${destination}; restoring the previous installation." >&2
	if [ -e "$destination" ] || [ -L "$destination" ]; then
		failed_dir="$(mktemp -d "$trash_root/Kent.app.failed.XXXXXX")"
		if ! mv "$destination" "$failed_dir/Kent.app"; then
			echo "Could not move the partial installation to Trash; manual cleanup is required at ${destination}." >&2
			return "$install_status"
		fi
	fi
	if [ -n "$previous_dir" ]; then
		if ! mv "$previous_dir/Kent.app" "$destination"; then
			echo "Could not restore the previous installation from ${previous_dir}/Kent.app." >&2
			return "$install_status"
		fi
		rmdir "$previous_dir" 2>/dev/null || true
	fi
	return "$install_status"
}

version=""
skip_install=0
install_app=0
require_updater_key=0
tauri_args=()

while [[ $# -gt 0 ]]; do
	case "$1" in
	--version)
		version="${2:-}"
		shift 2
		;;
	--skip-install)
		skip_install=1
		shift
		;;
	--install)
		install_app=1
		shift
		;;
	--require-updater-key)
		require_updater_key=1
		shift
		;;
	--)
		shift
		tauri_args=("$@")
		break
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

if [ "$install_app" = "1" ] && [ "$(uname -s)" != "Darwin" ]; then
	echo "Desktop installation is currently supported only on macOS." >&2
	exit 2
fi
if [ "$install_app" = "1" ] && [ "${#tauri_args[@]}" -ne 0 ]; then
	echo "Desktop installation does not accept forwarded Tauri build arguments." >&2
	exit 2
fi

if [ -z "$version" ]; then
	version="$(read_version)"
fi
version="${version#v}"

if [ -z "$version" ]; then
	echo "Unable to resolve version. Set --version, KENT_VERSION, or a VERSION file." >&2
	exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
	echo "pnpm is required to build the desktop app." >&2
	exit 2
fi

case "${CI:-}" in
1) export CI=true ;;
0) export CI=false ;;
esac

if [ "$skip_install" != "1" ]; then
	pnpm --dir apps install --frozen-lockfile
fi

echo "Building Kent desktop bundle version ${version}" >&2

compile_app_icon

if [ "$(uname -s)" = "Darwin" ] && [ "$require_updater_key" != "1" ] && ! tauri_args_select_bundles ${tauri_args[@]+"${tauri_args[@]}"}; then
	tauri_args=(--bundles app ${tauri_args[@]+"${tauri_args[@]}"})
	echo "Local macOS build defaults to the standalone .app bundle; release packaging builds installers." >&2
fi

# Updater artifact signing. tauri.conf.json sets bundle.createUpdaterArtifacts, so
# `tauri build` signs the updater artifacts and fails without the updater private
# key. Prefer an already-exported TAURI_SIGNING_PRIVATE_KEY (CI secret); otherwise
# fall back to the gitignored local key. Local bundle QA does not need updater
# artifacts, so when neither key exists we disable updater artifact generation via
# the Tauri config merge. Release packaging opts into a hard failure.
updater_key="$repo_root/.tauri/kent-desktop-updater.key"
create_updater_artifacts=true
if [ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
	if [ -f "$updater_key" ]; then
		TAURI_SIGNING_PRIVATE_KEY="$(cat "$updater_key")"
		export TAURI_SIGNING_PRIVATE_KEY
		export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
	elif [ "$require_updater_key" = "1" ]; then
		echo "Updater signing key missing. Set TAURI_SIGNING_PRIVATE_KEY (CI secret) or place the private key at .tauri/kent-desktop-updater.key." >&2
		exit 2
	else
		create_updater_artifacts=false
		echo "Updater signing key missing; building local desktop bundle without updater artifacts." >&2
	fi
fi

# Inject the macOS liquid-glass Assets.car into bundle.icon when it was generated
# above. It is intentionally absent from the committed bundle.icon so Linux/Windows
# builds — which neither generate nor consume the gitignored .car — don't choke on
# a missing/non-image icon path.
icon_car="apps/desktop/src-tauri/icons/Assets.car"
conf="apps/desktop/src-tauri/tauri.conf.json"
if ! command -v jq >/dev/null 2>&1; then
	echo "jq is required to build the desktop app." >&2
	exit 2
fi
if [ -f "$icon_car" ]; then
	icon_config="$(jq -c '.bundle.icon + ["icons/Assets.car"]' "$conf")"
else
	icon_config="$(jq -c '.bundle.icon' "$conf")"
fi
build_config="$(jq -cn \
	--arg v "$version" \
	--argjson icon "$icon_config" \
	--argjson createUpdaterArtifacts "$create_updater_artifacts" \
	'{version: $v, bundle: {icon: $icon, createUpdaterArtifacts: $createUpdaterArtifacts}}')"

pnpm --dir apps/desktop exec tauri build \
	--config "$build_config" \
	${tauri_args[@]+"${tauri_args[@]}"}

if [ "$(uname -s)" = "Darwin" ]; then
	build_profile="release"
	for arg in ${tauri_args[@]+"${tauri_args[@]}"}; do
		if [ "$arg" = "--debug" ] || [ "$arg" = "-d" ]; then
			build_profile="debug"
		fi
	done
	app_bundle="apps/desktop/src-tauri/target/${build_profile}/bundle/macos/Kent.app"

	if [ -z "${APPLE_SIGNING_IDENTITY:-}" ] && [ -d "$app_bundle" ]; then
		codesign --force --sign - --entitlements apps/desktop/src-tauri/entitlements.plist "$app_bundle"
		echo "Signed local macOS app bundle with ad-hoc identity sh.kent." >&2
	fi

	if [ "$install_app" = "1" ]; then
		install_macos_app "$app_bundle"
	fi
fi

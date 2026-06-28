#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
	cat <<'USAGE'
Usage: scripts/build-desktop.sh [--version vX.Y.Z|X.Y.Z] [--skip-install] [-- <tauri build args>]

Builds the Kent desktop (Tauri) app bundle. The bundle version is stamped from
VERSION (or KENT_VERSION / --version) at build time via a `tauri build --config`
merge, so the committed manifests (tauri.conf.json, package.json, Cargo.toml)
stay at their 0.0.0 placeholder and are never hand-edited per release.

Options:
  --version       Override the bundle version. Defaults to KENT_VERSION or VERSION.
  --skip-install  Skip the workspace dependency install step.
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

version=""
skip_install=0
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

if [ "$skip_install" != "1" ]; then
	pnpm --dir apps install --frozen-lockfile
fi

echo "Building Kent desktop bundle version ${version}" >&2

compile_app_icon

# Inject the macOS liquid-glass Assets.car into bundle.icon when it was generated
# above. It is intentionally absent from the committed bundle.icon so Linux/Windows
# builds — which neither generate nor consume the gitignored .car — don't choke on
# a missing/non-image icon path.
icon_car="apps/desktop/src-tauri/icons/Assets.car"
conf="apps/desktop/src-tauri/tauri.conf.json"
if [ -f "$icon_car" ]; then
	if ! command -v jq >/dev/null 2>&1; then
		echo "jq is required to inject the macOS app icon into the build config." >&2
		exit 2
	fi
	build_config="$(jq -cn \
		--arg v "$version" \
		--argjson icon "$(jq -c '.bundle.icon + ["icons/Assets.car"]' "$conf")" \
		'{version: $v, bundle: {icon: $icon}}')"
else
	build_config="{\"version\":\"${version}\"}"
fi

# Updater artifact signing. tauri.conf.json sets bundle.createUpdaterArtifacts, so
# `tauri build` signs the updater artifacts and fails without the updater private
# key. Prefer an already-exported TAURI_SIGNING_PRIVATE_KEY (CI secret); otherwise
# fall back to the gitignored local key. Fail clearly when neither is available.
updater_key="$repo_root/.tauri/kent-desktop-updater.key"
if [ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
	if [ -f "$updater_key" ]; then
		TAURI_SIGNING_PRIVATE_KEY="$(cat "$updater_key")"
		export TAURI_SIGNING_PRIVATE_KEY
		export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
	else
		echo "Updater signing key missing. Set TAURI_SIGNING_PRIVATE_KEY (CI secret) or place the private key at .tauri/kent-desktop-updater.key." >&2
		exit 2
	fi
fi

pnpm --dir apps/desktop exec tauri build \
	--config "$build_config" \
	${tauri_args[@]+"${tauri_args[@]}"}

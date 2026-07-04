#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
	cat <<'USAGE'
Usage: scripts/build.sh [target ...] [--output /path/to/kent] [--version vX.Y.Z|X.Y.Z] [--package ./cli/kent] [--skip-frontend]

Builds Kent repository targets.

Targets:
  server   Build the Go server/CLI binary. Selected by default.
  tui      Build the Rust TUI workspace. Selected by default.
  desktop  Build desktop frontend assets. Selected by default.

Options:
  --output   Output path for the Go server/CLI binary. Defaults to ./bin/kent when server is selected.
  --goout    Deprecated alias for --output.
  --version  Override the embedded Kent version. Defaults to KENT_VERSION or VERSION.
  --package  Main package to build. Defaults to ./cli/kent.
  --skip-frontend
            Skip desktop frontend asset build.

Examples:
  scripts/build.sh
  scripts/build.sh tui desktop
  scripts/build.sh server --output ./bin/kent
USAGE
}

read_version() {
	local version="${KENT_VERSION:-}"
	if [ -z "$version" ] && [ -f VERSION ]; then
		version="$(tr -d '[:space:]' <VERSION)"
	fi
	printf '%s' "${version#v}"
}

run_desktop_build() {
	if [ "${KENT_SKIP_FRONTEND:-0}" = "1" ]; then
		return
	fi
	if [ ! -f apps/package.json ]; then
		return
	fi
	if ! command -v pnpm >/dev/null 2>&1; then
		echo "pnpm is required to build desktop assets. Install pnpm or omit the desktop target." >&2
		exit 2
	fi

	local log_file
	log_file="$(mktemp -t kent-frontend-build.XXXXXX.log)"
	export npm_config_confirm_modules_purge=false
	if pnpm --dir apps install --frozen-lockfile >"$log_file" 2>&1 </dev/null &&
		pnpm --dir apps build >>"$log_file" 2>&1 </dev/null; then
		rm -f "$log_file"
		return
	fi
	cat "$log_file"
	rm -f "$log_file"
	exit 1
}

run_tui_build() {
	if [ ! -f tui-rs/Cargo.toml ]; then
		return
	fi
	cargo build --manifest-path tui-rs/Cargo.toml --workspace --locked
}

run_server_build() {
	if [ -z "$output" ]; then
		output="./bin/kent"
	fi

	if [ -z "$version" ]; then
		version="$(read_version)"
	fi
	version="${version#v}"

	mkdir -p "$(dirname -- "$output")"

	ldflags=(-s -w)
	if [ -n "$version" ]; then
		ldflags+=(-X "core/shared/config.Version=${version}")
	fi

	env CGO_ENABLED="${CGO_ENABLED:-0}" \
		go build \
		-trimpath \
		-buildvcs=false \
		-ldflags "${ldflags[*]}" \
		-o "$output" \
		"$package_path"
}

target_selected() {
	local expected="$1"
	local target
	for target in "${targets[@]}"; do
		if [ "$target" = "$expected" ]; then
			return 0
		fi
	done
	return 1
}

output=""
output_specified=0
package_path="./cli/kent"
package_specified=0
version=""
version_specified=0
skip_frontend="${KENT_SKIP_FRONTEND:-0}"
targets=()

while [[ $# -gt 0 ]]; do
	case "$1" in
	server | tui | desktop)
		targets+=("$1")
		shift
		;;
	--output | --goout)
		if [ $# -lt 2 ]; then
			echo "$1 requires an output path" >&2
			usage >&2
			exit 1
		fi
		output="$2"
		output_specified=1
		shift 2
		;;
	--version)
		if [ $# -lt 2 ]; then
			echo "--version requires a version value" >&2
			usage >&2
			exit 1
		fi
		version="$2"
		version_specified=1
		shift 2
		;;
	--package)
		if [ $# -lt 2 ]; then
			echo "--package requires a package path" >&2
			usage >&2
			exit 1
		fi
		package_path="$2"
		package_specified=1
		shift 2
		;;
	--skip-frontend)
		skip_frontend=1
		shift
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

if [ "${#targets[@]}" -eq 0 ]; then
	targets=(server tui desktop)
fi

export KENT_SKIP_FRONTEND="$skip_frontend"

if [ "$output_specified" -eq 1 ] && ! target_selected server; then
	echo "--output can only be used when building the server target" >&2
	usage >&2
	exit 1
fi

if [ "$package_specified" -eq 1 ] && ! target_selected server; then
	echo "--package can only be used when building the server target" >&2
	usage >&2
	exit 1
fi

if [ "$version_specified" -eq 1 ] && ! target_selected server; then
	echo "--version can only be used when building the server target" >&2
	usage >&2
	exit 1
fi

if target_selected desktop; then
	run_desktop_build
fi

if target_selected tui; then
	run_tui_build
fi

if target_selected server; then
	run_server_build
fi

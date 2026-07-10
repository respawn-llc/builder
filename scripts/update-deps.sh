#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

usage() {
	cat <<'USAGE'
Usage: scripts/update-deps.sh [--dry-run] [--skip-apps] [--skip-go] [--skip-docs] [--skip-rust]

Updates repository dependencies for the currently supported package managers:
  - root Go module (`go.mod` / `go.sum`)
  - desktop pnpm workspace (`apps/package.json` / `apps/pnpm-lock.yaml`)
  - docs pnpm workspace (`docs/package.json` / `docs/pnpm-lock.yaml`)
  - Rust Cargo workspaces (`Cargo.toml` / `Cargo.lock`)

GitHub Actions version pins are intentionally excluded from this script. Run this
script on the same 7-day dependency update cadence as the other ecosystems.

Options:
  --dry-run    Print planned update commands without executing them.
  --skip-apps  Skip desktop pnpm dependency updates.
  --skip-go    Skip Go module dependency updates.
  --skip-docs  Skip docs pnpm dependency updates.
  --skip-rust  Skip Rust Cargo dependency updates.
USAGE
}

dry_run="false"
skip_apps="false"
skip_go="false"
skip_docs="false"
skip_rust="false"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--dry-run)
		dry_run="true"
		shift
		;;
	--skip-apps)
		skip_apps="true"
		shift
		;;
	--skip-go)
		skip_go="true"
		shift
		;;
	--skip-docs)
		skip_docs="true"
		shift
		;;
	--skip-rust)
		skip_rust="true"
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

run_cmd() {
	if [[ "$dry_run" == "true" ]]; then
		printf '[dry-run]'
		printf ' %q' "$@"
		printf '\n'
		return
	fi
	"$@"
}

require_cmd() {
	local cmd="$1"
	if [[ "$dry_run" == "true" ]]; then
		return
	fi
	if command -v "$cmd" >/dev/null 2>&1; then
		return
	fi
	echo "Required command not found: $cmd" >&2
	exit 1
}

updated_any="false"

update_go_deps() {
	if [[ "$skip_go" == "true" ]]; then
		return
	fi
	if [[ ! -f "$repo_root/go.mod" ]]; then
		return
	fi
	require_cmd go
	echo "==> Updating Go module dependencies"
	run_cmd go get -u -t ./...
	run_cmd go mod tidy
	updated_any="true"
}

update_apps_deps() {
	if [[ "$skip_apps" == "true" ]]; then
		return
	fi
	if [[ ! -f "$repo_root/apps/package.json" ]]; then
		return
	fi
	require_cmd pnpm
	echo "==> Updating desktop pnpm dependencies"
	run_cmd pnpm --dir "$repo_root/apps" --recursive up --latest --lockfile-only
	# The lint parser supports TypeScript 6 only. Desktop build/typecheck scripts
	# invoke the stable TypeScript 7 alias explicitly.
	run_cmd pnpm --dir "$repo_root/apps" --filter @app/desktop --filter @app/native-bridge add --save-dev "typescript@^6.0.3" --lockfile-only
	updated_any="true"
}

update_docs_deps() {
	if [[ "$skip_docs" == "true" ]]; then
		return
	fi
	if [[ ! -f "$repo_root/docs/package.json" ]]; then
		return
	fi
	require_cmd pnpm
	echo "==> Updating docs pnpm dependencies"
	run_cmd pnpm --dir "$repo_root/docs" up --latest --lockfile-only
	updated_any="true"
}

update_rust_deps() {
	if [[ "$skip_rust" == "true" ]]; then
		return
	fi

	local manifests=(
		"$repo_root/tui-rs/Cargo.toml"
		"$repo_root/apps/desktop/src-tauri/Cargo.toml"
	)
	local manifest
	local updated_rust="false"

	for manifest in "${manifests[@]}"; do
		if [[ ! -f "$manifest" ]]; then
			continue
		fi
		if [[ ! -f "$(dirname "$manifest")/Cargo.lock" ]]; then
			continue
		fi
		if [[ "$updated_rust" != "true" ]]; then
			require_cmd cargo
			echo "==> Updating Rust Cargo dependencies"
			updated_rust="true"
		fi
		run_cmd cargo update --manifest-path "$manifest"
		updated_any="true"
	done
}

update_go_deps
update_apps_deps
update_docs_deps
update_rust_deps

if [[ "$updated_any" != "true" ]]; then
	echo "No supported dependency manifests found to update."
fi

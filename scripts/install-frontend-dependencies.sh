#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

if [ "${KENT_SKIP_FRONTEND:-0}" = "1" ] || [ ! -f apps/package.json ]; then
	exit 0
fi
if ! command -v pnpm >/dev/null 2>&1; then
	echo "pnpm is required to install frontend dependencies." >&2
	exit 2
fi

installed_lockfile="apps/node_modules/.pnpm/lock.yaml"
if [ -f apps/node_modules/.modules.yaml ] &&
	[ -f "$installed_lockfile" ] &&
	cmp -s apps/pnpm-lock.yaml "$installed_lockfile"; then
	exit 0
fi

export npm_config_confirm_modules_purge=false
pnpm --dir apps install --frozen-lockfile

#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$repo_root"

read_version() {
	local version="${KENT_VERSION:-}"
	if [ -z "$version" ] && [ -f VERSION ]; then
		version="$(tr -d ' \n' <VERSION)"
	fi
	printf '%s' "${version#v}"
}

run_format() {
	echo "==> verify formatting"
	local unformatted
	unformatted="$(gofmt -l .)"
	if [ -n "$unformatted" ]; then
		echo "The following files are not gofmt-formatted:"
		echo "$unformatted"
		exit 1
	fi
	if [ -f tui-rs/Cargo.toml ]; then
		cargo fmt --manifest-path tui-rs/Cargo.toml --all -- --check
	fi
}

run_frontend_deps_policy() {
	if [ ! -f apps/scripts/check-dependency-policy.mjs ]; then
		return
	fi
	echo "==> frontend dependency policy"
	if ! command -v node >/dev/null 2>&1; then
		echo "node is required to check frontend dependency policy" >&2
		exit 2
	fi
	node apps/scripts/check-dependency-policy.mjs
}

run_vet() {
	echo "==> go vet"
	go vet ./...
}

run_build() {
	echo "==> go build"
	local version
	version="$(read_version)"
	if [ -n "$version" ]; then
		KENT_VERSION="$version" bash scripts/build.sh --output ./bin/kent
		return
	fi
	bash scripts/build.sh --output ./bin/kent
}

run_test() {
	echo "==> test"
	./scripts/test.sh "$@"
}

run_rust_policy() {
	if [ ! -f tui-rs/Cargo.toml ]; then
		return
	fi
	echo "==> rust policy"
	cargo run --manifest-path tui-rs/Cargo.toml --locked -p manifest-check -- check --repo-root "$repo_root"
}

mode="${1:-all}"

case "$mode" in
all)
	run_frontend_deps_policy
	run_format
	run_rust_policy
	run_vet
	run_build
	run_test
	;;
deps)
	run_frontend_deps_policy
	;;
format)
	run_format
	;;
rust-policy)
	run_rust_policy
	;;
vet)
	run_vet
	;;
build)
	run_build
	;;
test)
	shift
	run_test "$@"
	;;
*)
	echo "Unknown mode: $mode" >&2
	echo "Usage: $0 [all|deps|format|rust-policy|vet|build|test [test target/options...]]" >&2
	exit 1
	;;
esac

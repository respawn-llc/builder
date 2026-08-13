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

run_frontend_lint() {
	if [ ! -f apps/package.json ]; then
		return
	fi
	echo "==> frontend dependencies"
	./scripts/install-frontend-dependencies.sh
	echo "==> frontend lint"
	pnpm --dir apps lint
}

run_vet() {
	echo "==> go vet"
	./scripts/generate-protobuf.sh run go -- go vet ./...
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
	./scripts/test.sh --full "$@"
}

run_rust_policy() {
	if [ ! -f tui-rs/Cargo.toml ]; then
		return
	fi
	echo "==> rust policy"
	cargo run --manifest-path tui-rs/Cargo.toml --locked -p manifest-check -- check --repo-root "$repo_root"
}

run_protobuf() {
	echo "==> Protobuf lint"
	(
		cd tools/protobuf
		go tool buf lint "$repo_root" --config "$repo_root/buf.yaml"
	)
	echo "==> Protobuf deterministic generation"
	./scripts/generate-protobuf.sh verify all
	echo "==> Protobuf migration lint"
	./scripts/generate-protobuf.sh run go -- go run ./shared/apicontract/cmd/migrationlint
	echo "==> Protobuf descriptor policy"
	./scripts/generate-protobuf.sh run go -- go test ./shared/apicontract/internal/migrationcheck \
		-run '^TestDescriptorPolicy' \
		-count=1
	echo "==> Protobuf schema/protocol version"
	./scripts/check-protobuf-schema-version.sh
}

mode="${1:-all}"

case "$mode" in
all)
	run_protobuf
	run_frontend_deps_policy
	run_frontend_lint
	run_format
	run_vet
	run_build
	run_test
	;;
deps)
	run_frontend_deps_policy
	;;
frontend-lint)
	run_frontend_lint
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
	run_protobuf
	run_test "$@"
	;;
protobuf)
	run_protobuf
	;;
*)
	echo "Unknown mode: $mode" >&2
	echo "Usage: $0 [all|deps|frontend-lint|format|rust-policy|vet|build|test [test target/options...]|protobuf]" >&2
	exit 1
	;;
esac

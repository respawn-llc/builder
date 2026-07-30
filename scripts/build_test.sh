#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d -t kent-build-test.XXXXXX)"
fake_bin="$test_root/bin"
output_dir="$test_root/output"
output_link="$output_dir/kent"
intermediate_link="$output_dir/kent-current"
output_target="$output_dir/kent-target"

cleanup() {
	unlink "$fake_bin/go" 2>/dev/null || true
	unlink "$output_link" 2>/dev/null || true
	unlink "$intermediate_link" 2>/dev/null || true
	unlink "$output_target" 2>/dev/null || true
	rmdir "$fake_bin" 2>/dev/null || true
	rmdir "$output_dir" 2>/dev/null || true
	rmdir "$test_root" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$fake_bin" "$output_dir"
printf 'previous build\n' >"$output_target"
ln -s "kent-target" "$intermediate_link"
ln -s "kent-current" "$output_link"

cat >"$fake_bin/go" <<'FAKE_GO'
#!/usr/bin/env bash

set -euo pipefail

output_found=0
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-o" ]; then
		if [ "$#" -lt 2 ]; then
			printf 'fake go: -o requires a path\n' >&2
			exit 2
		fi
		output="$2"
		output_found=1
		break
	fi
	shift
done

if [ "$output_found" != "1" ]; then
	printf 'fake go: missing -o\n' >&2
	exit 2
fi

unlink "$output" 2>/dev/null || true
printf 'protected build\n' >"$output"
chmod +x "$output"
FAKE_GO
chmod +x "$fake_bin/go"

PATH="$fake_bin:$PATH" \
	"$repo_root/scripts/build.sh" server --output "$output_link"

if [ ! -L "$output_link" ]; then
	printf 'build.sh replaced its symlink output\n' >&2
	exit 1
fi
if [ "$(readlink "$output_link")" != "kent-current" ]; then
	printf 'build.sh changed its symlink output target\n' >&2
	exit 1
fi
if [ ! -L "$intermediate_link" ] || [ "$(readlink "$intermediate_link")" != "kent-target" ]; then
	printf 'build.sh replaced an intermediate output symlink\n' >&2
	exit 1
fi
if [ "$(tr -d '\n' <"$output_target")" != "protected build" ]; then
	printf 'build.sh did not update the symlink target\n' >&2
	exit 1
fi

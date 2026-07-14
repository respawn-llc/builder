#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: scripts/update-brew-tap.sh [--version vX.Y.Z] [--tap /path/to/homebrew-tap] [--repo owner/name] [--formula name] [--desktop-url URL] [--commit] [--push]

Updates the Homebrew tap formula for kent with a new tag tarball + sha256, and
optionally the kent-desktop cask from a published .dmg asset.

Defaults:
  --version : $KENT_VERSION, $GITHUB_REF_NAME, or latest git tag in this repo
  --repo    : respawn-llc/kent
  --formula : kent
  --tap     : $KENT_TAP_PATH, $HOMEBREW_TAP_PATH, else ../homebrew-tap (relative to repo root)

Flags:
  --desktop-url : published macOS .dmg URL; when set, also (re)generate the cask
                  (downloaded to compute its sha256). Omit to update only the formula.
  --commit  : commit the formula/cask update in the tap repo
  --push    : push the commit (implies --commit)
USAGE
}

require_option_value() {
	local flag="$1"
	local value="${2:-}"
	if [[ -n "$value" && "$value" != --* ]]; then
		return
	fi
	echo "${flag} requires a value" >&2
	usage >&2
	exit 1
}

sha256_of() {
	local file="$1"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$file" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$file" | awk '{print $1}'
	else
		echo "sha256sum or shasum required" >&2
		exit 1
	fi
}

cask_versioned_url() {
	local source_url="$1"
	local cask_version="$2"
	local token='#{version}'
	local versioned_url="${source_url//${cask_version}/${token}}"
	if [[ "$versioned_url" == "$source_url" ]]; then
		echo "Desktop URL must contain cask version ${cask_version} so Homebrew can audit it as versioned: ${source_url}" >&2
		exit 1
	fi
	printf '%s' "$versioned_url"
}

version=""
repo="respawn-llc/kent"
formula="kent"
tap_dir=""
do_commit="false"
do_push="false"
desktop_url=""
# Constant cask name; never varies, so it is not a CLI flag.
desktop_cask="kent-desktop"

unset_git_local_env() {
	local config_count="${GIT_CONFIG_COUNT:-}"
	unset \
		GIT_ALTERNATE_OBJECT_DIRECTORIES \
		GIT_COMMON_DIR \
		GIT_CONFIG \
		GIT_CONFIG_COUNT \
		GIT_CONFIG_PARAMETERS \
		GIT_DIR \
		GIT_GLOB_PATHSPECS \
		GIT_GRAFT_FILE \
		GIT_ICASE_PATHSPECS \
		GIT_IMPLICIT_WORK_TREE \
		GIT_INDEX_FILE \
		GIT_INTERNAL_SUPER_PREFIX \
		GIT_LITERAL_PATHSPECS \
		GIT_NAMESPACE \
		GIT_NOGLOB_PATHSPECS \
		GIT_NO_REPLACE_OBJECTS \
		GIT_OBJECT_DIRECTORY \
		GIT_PREFIX \
		GIT_REPLACE_REF_BASE \
		GIT_SHALLOW_FILE \
		GIT_WORK_TREE

	if [[ -n "$config_count" && "$config_count" =~ ^[0-9]+$ ]]; then
		local i
		for ((i = 0; i < config_count; i++)); do
			unset "GIT_CONFIG_KEY_${i}" "GIT_CONFIG_VALUE_${i}"
		done
	fi
}

unset_git_local_env

while [[ $# -gt 0 ]]; do
	case "$1" in
	--version)
		require_option_value "--version" "${2:-}"
		version="$2"
		shift 2
		;;
	--repo)
		require_option_value "--repo" "${2:-}"
		repo="$2"
		shift 2
		;;
	--formula)
		require_option_value "--formula" "${2:-}"
		formula="$2"
		shift 2
		;;
	--desktop-url)
		require_option_value "--desktop-url" "${2:-}"
		desktop_url="$2"
		shift 2
		;;
	--tap)
		require_option_value "--tap" "${2:-}"
		tap_dir="$2"
		shift 2
		;;
	--commit)
		do_commit="true"
		shift
		;;
	--push)
		do_commit="true"
		do_push="true"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "Unknown arg: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

if ! repo_root="$(git rev-parse --show-toplevel 2>/dev/null)"; then
	echo "Not inside a git repo" >&2
	exit 1
fi

if [[ -z "$version" ]]; then
	if [[ -n "${KENT_VERSION:-}" ]]; then
		version="${KENT_VERSION}"
	elif [[ -n "${GITHUB_REF_NAME:-}" ]]; then
		version="${GITHUB_REF_NAME}"
	elif [[ -n "${GITHUB_REF:-}" ]]; then
		version="${GITHUB_REF##*/}"
	else
		version="$(git -C "$repo_root" describe --tags --abbrev=0)"
	fi
fi

if [[ -z "$tap_dir" ]]; then
	if [[ -n "${KENT_TAP_PATH:-}" ]]; then
		tap_dir="$KENT_TAP_PATH"
	elif [[ -n "${HOMEBREW_TAP_PATH:-}" ]]; then
		tap_dir="$HOMEBREW_TAP_PATH"
	elif [[ -d "$repo_root/../homebrew-tap" ]]; then
		tap_dir="$repo_root/../homebrew-tap"
	else
		echo "Tap repo not found. Provide --tap or set HOMEBREW_TAP_PATH" >&2
		exit 1
	fi
fi

formula_path="$tap_dir/Formula/${formula}.rb"
formula_class="$(printf '%s\n' "$formula" | awk -F'[-_]' '{for (i = 1; i <= NF; i++) if ($i != "") printf toupper(substr($i, 1, 1)) substr($i, 2)}')"
case "$version" in
v*) tag="$version" ;;
*) tag="v$version" ;;
esac
bare_version="${tag#v}"
release_base="https://github.com/${repo}/releases/download/${tag}"

tmp_file="$(mktemp)"
tmp_formula="$(mktemp)"
tmp_cask=""
cleanup() {
	rm -f "$tmp_file" "$tmp_formula" "$tmp_cask"
}
trap cleanup EXIT

binary_sha256() {
	curl -fsSL "${release_base}/$1" -o "$tmp_file"
	sha256_of "$tmp_file"
}

darwin_arm64_sha256="$(binary_sha256 "kent_${bare_version}_darwin_arm64.tar.gz")"
linux_arm64_sha256="$(binary_sha256 "kent_${bare_version}_linux_arm64.tar.gz")"
linux_amd64_sha256="$(binary_sha256 "kent_${bare_version}_linux_amd64.tar.gz")"

mkdir -p "$(dirname "$formula_path")"
cat >"$tmp_formula" <<EOF
class ${formula_class} < Formula
  desc "Minimal terminal coding agent for professional engineering workflows"
  homepage "https://github.com/respawn-llc/kent"
  version "$bare_version"
  license "AGPL-3.0-only"

  bottle do
    root_url "https://ghcr.io/v2/respawn-llc/tap"
  end

  depends_on "ripgrep"

  on_macos do
    depends_on arch: :arm64

    on_arm do
      url "${release_base}/kent_${bare_version}_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64_sha256"
    end
  end

  on_linux do
    on_arm do
      url "${release_base}/kent_${bare_version}_linux_arm64.tar.gz"
      sha256 "$linux_arm64_sha256"
    end
    on_intel do
      url "${release_base}/kent_${bare_version}_linux_amd64.tar.gz"
      sha256 "$linux_amd64_sha256"
    end
  end

  def install
    os = OS.mac? ? "darwin" : "linux"
    arch = Hardware::CPU.arm? ? "arm64" : "amd64"
    bin.install "kent_#{version}_#{os}_#{arch}" => "kent"
  end

  def post_install
    output = Utils.safe_popen_read(bin/"kent", "service", "restart", "--if-installed").strip
    ohai output unless output.empty?
  rescue => e
    opoo "Kent background service restart failed after update: #{e.message}"
  end

  def caveats
    <<~EOS
      Homebrew does not install the Kent server background service.

      If you want one shared background server for all Kent frontends (~70 MB RAM), run:
        kent service install
    EOS
  end

  test do
    assert_match "Usage of kent:", shell_output("#{bin}/kent --help 2>&1")
  end
end
EOF

if [[ ! -f "$formula_path" ]] || ! cmp -s "$tmp_formula" "$formula_path"; then
	mv "$tmp_formula" "$formula_path"
	tmp_formula="$(mktemp)"
fi

chmod 0644 "$formula_path"

cask_path=""
if [[ -n "$desktop_url" ]]; then
	cask_version="${version#v}"
	cask_path="$tap_dir/Casks/${desktop_cask}.rb"
	tmp_cask="$(mktemp)"
	curl -fsSL "$desktop_url" -o "$tmp_file"
	dmg_sha256="$(sha256_of "$tmp_file")"
	cask_url="$(cask_versioned_url "$desktop_url" "$cask_version")"
	mkdir -p "$(dirname "$cask_path")"
	cat >"$tmp_cask" <<EOF
cask "${desktop_cask}" do
  version "${cask_version}"
  sha256 "${dmg_sha256}"

  url "${cask_url}"
  name "Kent"
  desc "Desktop client for the Kent coding agent"
  homepage "https://github.com/respawn-llc/kent"

  depends_on formula: "kent"
  depends_on macos: :sequoia
  depends_on arch: :arm64

  app "Kent.app"

  # Self-update is install-source-aware: Homebrew owns updates for cask installs
  # and locksteps with the kent formula, so the in-app updater is gated off by
  # writing the desktop settings file. Do NOT add \`auto_updates true\` — that would
  # make \`brew upgrade\` skip this cask and let the app self-update ahead of the
  # server. See docs/dev/specs/release-distribution.md.
  postflight do
    require "json"
    settings_path = File.expand_path("~/Library/Application Support/sh.kent/settings.json")
    FileUtils.mkdir_p(File.dirname(settings_path))
    data = {}
    if File.exist?(settings_path)
      begin
        parsed = JSON.parse(File.read(settings_path))
        data = parsed if parsed.is_a?(Hash)
      rescue JSON::ParserError
        data = {}
      end
    end
    data["version"] = 1
    data["selfUpdate"] = "disabled"
    File.write(settings_path, "#{JSON.pretty_generate(data)}\n")
  end

  uninstall quit: "sh.kent"

  zap trash: [
    "~/Library/Application Support/sh.kent",
    "~/Library/Caches/sh.kent",
    "~/Library/HTTPStorages/sh.kent",
    "~/Library/Saved Application State/sh.kent.savedState",
    "~/Library/WebKit/sh.kent",
  ]
end
EOF
	if [[ ! -f "$cask_path" ]] || ! cmp -s "$tmp_cask" "$cask_path"; then
		mv "$tmp_cask" "$cask_path"
		tmp_cask="$(mktemp)"
	fi
	chmod 0644 "$cask_path"
fi

if [[ "$do_commit" == "true" ]]; then
	git -C "$tap_dir" add "$formula_path"
	if [[ -n "$cask_path" ]]; then
		git -C "$tap_dir" add "$cask_path"
	fi
	if git -C "$tap_dir" diff --cached --quiet; then
		echo "No formula changes to commit"
	else
		git -C "$tap_dir" commit -m "${formula} ${version}${cask_path:+ + ${desktop_cask}}"
	fi
fi

if [[ "$do_push" == "true" ]]; then
	git -C "$tap_dir" push
fi

echo "Updated ${formula_path}"
echo "  darwin_arm64 sha256: $darwin_arm64_sha256"
echo "  linux_arm64 sha256:  $linux_arm64_sha256"
echo "  linux_amd64 sha256:  $linux_amd64_sha256"
if [[ -n "$cask_path" ]]; then
	echo "Updated ${cask_path}"
	echo "  url: $cask_url"
	echo "  sha256: $dmg_sha256"
fi

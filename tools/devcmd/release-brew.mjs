import { createHash } from "node:crypto";
import { chmod, mkdir, stat, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import {
  CommandError,
  capture,
  main,
  normalizeVersion,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
} from "./runtime.mjs";

await main(async () => {
  const { values } = parseArgs({
    args: process.argv.slice(2),
    options: {
      version: { type: "string" },
      tap: { type: "string" },
      repo: { type: "string", default: "respawn-llc/kent" },
      formula: { type: "string", default: "kent" },
      "desktop-url": { type: "string" },
      commit: { type: "boolean", default: false },
      push: { type: "boolean", default: false },
    },
  });
  await requireExecutable("git");
  const gitEnvironment = isolatedGitEnvironment();
  const version = await resolveVersion(values.version, gitEnvironment);
  const bareVersion = normalizeVersion(version);
  const tag = `v${bareVersion}`;
  const tap = resolveTap(values.tap);
  await requireDirectory(tap);
  const releaseBase = `https://github.com/${values.repo}/releases/download/${tag}`;

  const checksums = {};
  for (const target of ["darwin_arm64", "linux_arm64", "linux_amd64"]) {
    checksums[target] = await remoteSha256(
      `${releaseBase}/kent_${bareVersion}_${target}.tar.gz`,
    );
  }
  const formulaPath = resolve(tap, "Formula", `${values.formula}.rb`);
  await mkdir(dirname(formulaPath), { recursive: true });
  await writeFile(
    formulaPath,
    formulaContents(values.formula, bareVersion, releaseBase, checksums),
    { mode: 0o644 },
  );
  await chmod(formulaPath, 0o644);

  let caskPath;
  if (values["desktop-url"]) {
    const caskVersion = bareVersion;
    const caskUrl = versionedCaskUrl(values["desktop-url"], caskVersion);
    const checksum = await remoteSha256(values["desktop-url"]);
    caskPath = resolve(tap, "Casks", "kent-desktop.rb");
    await mkdir(dirname(caskPath), { recursive: true });
    await writeFile(caskPath, caskContents(caskVersion, checksum, caskUrl), {
      mode: 0o644,
    });
    await chmod(caskPath, 0o644);
  }

  if (values.commit || values.push) {
    await run("git", ["-C", tap, "add", formulaPath, ...(caskPath ? [caskPath] : [])], {
      env: gitEnvironment,
    });
    const diff = await run("git", ["-C", tap, "diff", "--cached", "--quiet"], {
      env: gitEnvironment,
      allowFailure: true,
    });
    if (diff.code !== 0) {
      await run(
        "git",
        [
          "-C",
          tap,
          "commit",
          "-m",
          `${values.formula} ${version}${caskPath ? " + kent-desktop" : ""}`,
        ],
        { env: gitEnvironment },
      );
    }
  }
  if (values.push) await run("git", ["-C", tap, "push"], { env: gitEnvironment });
});

async function resolveVersion(explicit, environment) {
  if (explicit) return explicit;
  if (process.env.KENT_VERSION) return process.env.KENT_VERSION;
  if (process.env.GITHUB_REF_NAME) return process.env.GITHUB_REF_NAME;
  if (process.env.GITHUB_REF) {
    return process.env.GITHUB_REF.slice(process.env.GITHUB_REF.lastIndexOf("/") + 1);
  }
  const result = await capture(
    "git",
    ["-C", repoRoot, "describe", "--tags", "--abbrev=0"],
    { env: environment },
  );
  return result.trim();
}

function resolveTap(explicit) {
  if (explicit) return resolve(explicit);
  if (process.env.KENT_TAP_PATH) return resolve(process.env.KENT_TAP_PATH);
  if (process.env.HOMEBREW_TAP_PATH) return resolve(process.env.HOMEBREW_TAP_PATH);
  return resolve(repoRoot, "..", "homebrew-tap");
}

async function remoteSha256(url) {
  const response = await fetch(url);
  if (!response.ok) throw new CommandError(`download ${url}: HTTP ${response.status}`);
  if (!response.body) throw new CommandError(`download ${url}: response body is missing`);
  const hash = createHash("sha256");
  for await (const chunk of response.body) hash.update(chunk);
  return hash.digest("hex");
}

function formulaContents(name, version, base, sha) {
  const className = name
    .split("-")
    .flatMap((part) => part.split("_"))
    .filter(Boolean)
    .map((part) => part[0].toUpperCase() + part.slice(1))
    .join("");
  return `class ${className} < Formula
  desc "Minimal terminal coding agent for professional engineering workflows"
  homepage "https://github.com/respawn-llc/kent"
  url "${base}/kent_${version}_darwin_arm64.tar.gz"
  sha256 "${sha.darwin_arm64}"
  license "AGPL-3.0-only"

  bottle do
    root_url "https://ghcr.io/v2/respawn-llc/tap"
  end

  depends_on "ripgrep"

  on_macos do
    depends_on arch: :arm64
  end

  on_linux do
    on_arm do
      url "${base}/kent_${version}_linux_arm64.tar.gz"
      sha256 "${sha.linux_arm64}"
    end
    on_intel do
      url "${base}/kent_${version}_linux_amd64.tar.gz"
      sha256 "${sha.linux_amd64}"
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
`;
}

function caskContents(version, sha256, url) {
  return `cask "kent-desktop" do
  version "${version}"
  sha256 "${sha256}"

  url "${url}"
  name "Kent"
  desc "Desktop client for the Kent coding agent"
  homepage "https://github.com/respawn-llc/kent"

  depends_on formula: "kent"
  depends_on macos: :sequoia
  depends_on arch: :arm64

  app "Kent.app"

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
    File.write(settings_path, "#{JSON.pretty_generate(data)}\\n")
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
`;
}

function versionedCaskUrl(url, version) {
  const parsed = new URL(url);
  if (parsed.search !== "" || parsed.hash !== "") {
    throw new CommandError("Desktop URL must not contain a query or fragment.");
  }
  const pathSegments = parsed.pathname.split("/");
  const filename = pathSegments.pop();
  const expectedFilename = `Kent_${version}_aarch64.dmg`;
  if (filename !== expectedFilename) {
    throw new CommandError(`Desktop URL must end with ${expectedFilename}.`);
  }
  pathSegments.push("Kent_#{version}_aarch64.dmg");
  return `${parsed.origin}${pathSegments.join("/")}`;
}

function isolatedGitEnvironment() {
  const environment = { ...process.env };
  const configCount = Number(environment.GIT_CONFIG_COUNT || "0");
  for (const name of [
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_COMMON_DIR",
    "GIT_CONFIG",
    "GIT_CONFIG_COUNT",
    "GIT_CONFIG_PARAMETERS",
    "GIT_DIR",
    "GIT_GLOB_PATHSPECS",
    "GIT_GRAFT_FILE",
    "GIT_ICASE_PATHSPECS",
    "GIT_IMPLICIT_WORK_TREE",
    "GIT_INDEX_FILE",
    "GIT_INTERNAL_SUPER_PREFIX",
    "GIT_LITERAL_PATHSPECS",
    "GIT_NAMESPACE",
    "GIT_NOGLOB_PATHSPECS",
    "GIT_NO_REPLACE_OBJECTS",
    "GIT_OBJECT_DIRECTORY",
    "GIT_PREFIX",
    "GIT_REPLACE_REF_BASE",
    "GIT_SHALLOW_FILE",
    "GIT_WORK_TREE",
  ]) {
    delete environment[name];
  }
  if (Number.isInteger(configCount) && configCount >= 0) {
    for (let index = 0; index < configCount; index++) {
      delete environment[`GIT_CONFIG_KEY_${index}`];
      delete environment[`GIT_CONFIG_VALUE_${index}`];
    }
  }
  return environment;
}

async function requireDirectory(path) {
  try {
    if (!(await stat(path)).isDirectory()) throw new Error();
  } catch {
    throw new CommandError(`Tap repo not found: ${path}`, 2);
  }
}

import { cp, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import {
  CommandError,
  fileSha256,
  hostPlatform,
  main,
  normalizeVersion,
  parseArgs,
  readVersion,
  repoRoot,
} from "./runtime.mjs";
import { buildDesktop } from "./desktop.mjs";

await main(async () => {
  const [operation, ...args] = process.argv.slice(2);
  const { values } = parseArgs({
    args,
    options: {
      version: { type: "string" },
      "dist-dir": { type: "string", default: "dist/desktop" },
      "base-url": { type: "string" },
      "pub-date": { type: "string" },
      notes: { type: "string", default: "See the release notes." },
    },
  });
  const version = normalizeVersion(values.version || (await readVersion()));
  const distDir = resolve(repoRoot, values["dist-dir"]);
  if (operation === "build" || operation === "release") {
    await build(version, distDir);
  } else if (operation === "assemble") {
    await assemble(version, distDir, values);
  } else {
    throw new CommandError("unknown desktop release operation", 2);
  }
});

async function build(version, distDir) {
  await buildDesktop({ version, requireUpdaterKey: true });
  await mkdir(distDir, { recursive: true });
  const bundle = resolve(repoRoot, "apps/desktop/src-tauri/target/release/bundle");
  const artifacts = hostPlatform.darwin
    ? [
        [`dmg/Kent_${version}_aarch64.dmg`, `Kent_${version}_aarch64.dmg`],
        ["macos/Kent.app.tar.gz", `Kent_${version}_aarch64.app.tar.gz`],
        ["macos/Kent.app.tar.gz.sig", `Kent_${version}_aarch64.app.tar.gz.sig`],
      ]
    : hostPlatform.linux
      ? [
          [`appimage/Kent_${version}_amd64.AppImage`, `Kent_${version}_amd64.AppImage`],
          [
            `appimage/Kent_${version}_amd64.AppImage.sig`,
            `Kent_${version}_amd64.AppImage.sig`,
          ],
          [`deb/Kent_${version}_amd64.deb`, `Kent_${version}_amd64.deb`],
        ]
      : hostPlatform.windows
        ? [
            [`nsis/Kent_${version}_x64-setup.exe`, `Kent_${version}_x64-setup.exe`],
            [
              `nsis/Kent_${version}_x64-setup.exe.sig`,
              `Kent_${version}_x64-setup.exe.sig`,
            ],
          ]
        : undefined;
  if (!artifacts) throw new CommandError("unsupported desktop release host", 2);
  for (const [source, destination] of artifacts) {
    await requireFile(resolve(bundle, source));
    await cp(resolve(bundle, source), resolve(distDir, destination));
  }
}

async function assemble(version, distDir, values) {
  const names = {
    mac: `Kent_${version}_aarch64.app.tar.gz`,
    linux: `Kent_${version}_amd64.AppImage`,
    windows: `Kent_${version}_x64-setup.exe`,
  };
  for (const name of [
    `Kent_${version}_aarch64.dmg`,
    names.mac,
    `${names.mac}.sig`,
    names.linux,
    `${names.linux}.sig`,
    `Kent_${version}_amd64.deb`,
    names.windows,
    `${names.windows}.sig`,
  ]) {
    await requireFile(resolve(distDir, name));
  }
  const baseUrl =
    values["base-url"] ||
    `https://github.com/respawn-llc/kent/releases/download/v${version}`;
  const signature = async (name) => (await readFile(resolve(distDir, `${name}.sig`), "utf8")).trim();
  const manifest = {
    version,
    notes: values.notes,
    pub_date: values["pub-date"] || formatUtcSecond(new Date()),
    platforms: {
      "darwin-aarch64": { signature: await signature(names.mac), url: `${baseUrl}/${names.mac}` },
      "linux-x86_64": {
        signature: await signature(names.linux),
        url: `${baseUrl}/${names.linux}`,
      },
      "windows-x86_64": {
        signature: await signature(names.windows),
        url: `${baseUrl}/${names.windows}`,
      },
    },
  };
  const distributable = [
    `Kent_${version}_aarch64.dmg`,
    names.mac,
    names.linux,
    `Kent_${version}_amd64.deb`,
    names.windows,
  ].sort();
  const checksums = [];
  for (const name of distributable) {
    checksums.push(`${await fileSha256(resolve(distDir, name))}  ${name}`);
  }
  await writeFile(resolve(distDir, "latest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(resolve(distDir, "desktop-checksums.txt"), `${checksums.join("\n")}\n`);
}

function formatUtcSecond(date) {
  const component = (value, width = 2) => String(value).padStart(width, "0");
  return [
    component(date.getUTCFullYear(), 4),
    "-",
    component(date.getUTCMonth() + 1),
    "-",
    component(date.getUTCDate()),
    "T",
    component(date.getUTCHours()),
    ":",
    component(date.getUTCMinutes()),
    ":",
    component(date.getUTCSeconds()),
    "Z",
  ].join("");
}

async function requireFile(path) {
  try {
    if (!(await stat(path)).isFile()) throw new Error();
  } catch {
    throw new CommandError(`expected bundle not found: ${path}`);
  }
}

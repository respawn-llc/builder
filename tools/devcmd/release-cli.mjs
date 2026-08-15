import { cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { arch, tmpdir } from "node:os";
import { basename, resolve } from "node:path";
import {
  CommandError,
  capture,
  fileSha256,
  hostPlatform,
  main,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
  withTemporaryDirectory,
  normalizeVersion,
} from "./runtime.mjs";

const targets = [
  { goos: "darwin", goarch: "arm64", archiveExt: "tar.gz", binaryExt: "" },
  { goos: "linux", goarch: "amd64", archiveExt: "tar.gz", binaryExt: "" },
  { goos: "linux", goarch: "arm64", archiveExt: "tar.gz", binaryExt: "" },
  { goos: "windows", goarch: "amd64", archiveExt: "zip", binaryExt: ".exe" },
  { goos: "windows", goarch: "arm64", archiveExt: "zip", binaryExt: ".exe" },
];

await main(async () => {
  const [operation, ...args] = process.argv.slice(2);
  const options = releaseOptions(args, operation);
  if (operation === "release") {
    await build(options);
    await verifyManifest(options);
    await verifyLinuxStatic(options);
  } else if (operation === "build") await build(options);
  else if (operation === "verify-manifest") await verifyManifest(options);
  else if (operation === "verify-linux-static") await verifyLinuxStatic(options);
  else if (operation === "smoke") await smoke(options);
  else if (operation === "windows-installer-smoke") await windowsInstallerSmoke(options);
  else throw new CommandError("unknown CLI release operation", 2);
});

function releaseOptions(args, operation) {
  const { values } = parseArgs({
    args,
    options: {
      version: { type: "string" },
      "dist-dir": { type: "string", default: "dist" },
      goos: { type: "string" },
      goarch: { type: "string" },
      "archive-ext": { type: "string" },
      "binary-ext": { type: "string", default: "" },
    },
  });
  if (!values.version) {
    throw new CommandError("--version is required", 2);
  }
  if (operation === "smoke") {
    requireChoice(values.goos, "--goos", ["darwin", "linux", "windows"]);
    requireChoice(values.goarch, "--goarch", ["amd64", "arm64"]);
    requireChoice(values["archive-ext"], "--archive-ext", ["tar.gz", "zip"]);
  }
  if (operation === "windows-installer-smoke") {
    requireChoice(values.goarch, "--goarch", ["amd64", "arm64"]);
  }
  return {
    ...values,
    version: values.version ? normalizeVersion(values.version) : undefined,
    distDir: resolve(repoRoot, values["dist-dir"]),
  };
}

async function build(options) {
  await requireExecutable("just");
  await requireExecutable("tar");
  if (!hostPlatform.windows) await requireExecutable("zip");
  await mkdir(options.distDir, { recursive: true });
  const archives = archiveNames(options.version);
  for (const name of [...archives, "checksums.txt"]) {
    await rm(resolve(options.distDir, name), { force: true });
  }

  await withTemporaryDirectory("kent-cli-release-", async (staging) => {
    for (const target of targets) {
      const base = `kent_${options.version}_${target.goos}_${target.goarch}`;
      const binary = resolve(staging, `${base}${target.binaryExt}`);
      await run("just", [
        "build",
        "_go",
        "--output",
        binary,
        "--version",
        options.version,
        "--goos",
        target.goos,
        "--goarch",
        target.goarch,
      ]);
      const archive = resolve(options.distDir, `${base}.${target.archiveExt}`);
      if (target.archiveExt === "zip") {
        if (hostPlatform.windows) {
          await run("tar", ["-a", "-cf", archive, basename(binary)], { cwd: staging });
        } else {
          await run("zip", ["-q", archive, basename(binary)], { cwd: staging });
        }
      } else {
        await run("tar", ["-czf", archive, basename(binary)], { cwd: staging });
      }
    }
  });
  const lines = [];
  for (const name of [...archives].sort()) {
    lines.push(`${await fileSha256(resolve(options.distDir, name))}  ${name}`);
  }
  await writeFile(resolve(options.distDir, "checksums.txt"), `${lines.join("\n")}\n`);
}

async function verifyManifest(options) {
  const lines = [];
  for (const name of archiveNames(options.version).sort()) {
    lines.push(`${await fileSha256(resolve(options.distDir, name))}  ${name}`);
  }
  const expected = `${lines.join("\n")}\n`;
  const actual = await readFile(resolve(options.distDir, "checksums.txt"), "utf8");
  if (actual !== expected) {
    throw new CommandError("release checksum manifest does not match the expected target matrix");
  }
}

function archiveNames(version) {
  return targets.map(
    (target) =>
      `kent_${version}_${target.goos}_${target.goarch}.${target.archiveExt}`,
  );
}

async function verifyLinuxStatic(options) {
  await requireExecutable("tar");
  await withTemporaryDirectory("kent-linux-static-", async (staging) => {
    for (const goarch of ["amd64", "arm64"]) {
      const base = `kent_${options.version}_linux_${goarch}`;
      await run("tar", [
        "-xzf",
        resolve(options.distDir, `${base}.tar.gz`),
        "-C",
        staging,
      ]);
      const binary = await readFile(resolve(staging, base));
      if (elfHasInterpreter(binary)) {
        throw new CommandError(`Dynamic linking is not allowed for ${base}.`);
      }
    }
  });
}

function elfHasInterpreter(binary) {
  if (
    binary.length < 64 ||
    binary[0] !== 0x7f ||
    binary[1] !== 0x45 ||
    binary[2] !== 0x4c ||
    binary[3] !== 0x46
  ) {
    throw new CommandError("release binary is not ELF");
  }
  const littleEndian = binary[5] === 1;
  const read16 = littleEndian ? Buffer.prototype.readUInt16LE : Buffer.prototype.readUInt16BE;
  const read32 = littleEndian ? Buffer.prototype.readUInt32LE : Buffer.prototype.readUInt32BE;
  const is64 = binary[4] === 2;
  const tableOffset = is64
    ? Number(littleEndian ? binary.readBigUInt64LE(32) : binary.readBigUInt64BE(32))
    : read32.call(binary, 28);
  const entrySize = read16.call(binary, is64 ? 54 : 42);
  const entryCount = read16.call(binary, is64 ? 56 : 44);
  for (let index = 0; index < entryCount; index++) {
    if (read32.call(binary, tableOffset + index * entrySize) === 3) return true;
  }
  return false;
}

async function smoke(options) {
  const expectedPlatform = { darwin: "darwin", linux: "linux", windows: "win32" }[
    options.goos
  ];
  const expectedArch = { amd64: "x64", arm64: "arm64" }[options.goarch];
  if (process.platform !== expectedPlatform || arch() !== expectedArch) {
    throw new CommandError("release smoke target does not match this host", 2);
  }
  const base = `kent_${options.version}_${options.goos}_${options.goarch}`;
  await withTemporaryDirectory("kent-cli-smoke-", async (staging) => {
    const archive = resolve(options.distDir, `${base}.${options["archive-ext"]}`);
    if (options["archive-ext"] === "zip") {
      await run(hostPlatform.windows ? "tar" : "unzip", [
        hostPlatform.windows ? "-xf" : "-q",
        archive,
        ...(hostPlatform.windows ? ["-C", staging] : ["-d", staging]),
      ]);
    } else if (options["archive-ext"] === "tar.gz") {
      await run("tar", ["-xzf", archive, "-C", staging]);
    } else {
      throw new CommandError("unsupported archive extension", 2);
    }
    const binary = resolve(staging, `${base}${options["binary-ext"]}`);
    const version = (await capture(binary, ["--version"])).trim();
    if (version !== options.version) throw new CommandError(`unexpected version: ${version}`);
    await run(binary, ["--help"], { stdio: "ignore" });
  });
}

async function windowsInstallerSmoke(options) {
  if (!hostPlatform.windows) throw new CommandError("Windows installer smoke requires Windows", 2);
  for (const executable of ["powershell", "go", "tar"]) {
    await requireExecutable(executable);
  }
  await withTemporaryDirectory("kent-windows-installer-", async (staging) => {
    const releaseBase = resolve(staging, "releases");
    const releaseDirectory = resolve(releaseBase, `v${options.version}`);
    const installDirectory = resolve(staging, "install");
    await mkdir(releaseDirectory, { recursive: true });
    await mkdir(installDirectory);
    for (const goarch of ["amd64", "arm64"]) {
      const name = `kent_${options.version}_windows_${goarch}.zip`;
      await cp(resolve(options.distDir, name), resolve(releaseDirectory, name));
    }
    await cp(
      resolve(options.distDir, "checksums.txt"),
      resolve(releaseDirectory, "checksums.txt"),
    );
    const script = resolve(repoRoot, "scripts/install.ps1");
    await run(
      "powershell",
      [
        "-NoProfile",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        script,
        "-Version",
        options.version,
        "-InstallDir",
        installDirectory,
        "-Yes",
        "-NoPath",
        "-NoDeps",
        "-NoServiceRestart",
      ],
      { env: { ...process.env, KENT_RELEASE_BASE: releaseBase } },
    );
    await run("powershell", [
      "-NoProfile",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      script,
      "-Uninstall",
      "-InstallDir",
      installDirectory,
      "-Yes",
    ]);

    await verifyStoppedServiceRestoration(options, staging, script);
  });
}

function requireChoice(value, name, choices) {
  if (!choices.includes(value)) {
    throw new CommandError(`${name} must be one of: ${choices.join(", ")}`, 2);
  }
}

async function verifyStoppedServiceRestoration(options, staging, script) {
  const releaseBase = resolve(staging, "recovery-release");
  const releaseDirectory = resolve(releaseBase, `v${options.version}`);
  const installDirectory = resolve(staging, "recovery-install");
  const binaryDirectory = resolve(staging, "recovery-binary");
  const marker = resolve(staging, "recovery-marker.txt");
  const source = resolve(staging, "fake-kent.go");
  const binary = resolve(installDirectory, "kent.exe");
  const archiveBinary = resolve(
    binaryDirectory,
    `kent_${options.version}_windows_${options.goarch}.exe`,
  );
  const archive = resolve(
    releaseDirectory,
    `kent_${options.version}_windows_${options.goarch}.zip`,
  );

  await Promise.all([
    mkdir(releaseDirectory, { recursive: true }),
    mkdir(installDirectory, { recursive: true }),
    mkdir(binaryDirectory, { recursive: true }),
  ]);
  await writeFile(source, fakeKentSource);
  const goEnvironment = {
    ...process.env,
    GOOS: "windows",
    GOARCH: options.goarch,
  };
  await run("go", ["build", "-o", binary, source], { env: goEnvironment });
  await cp(binary, archiveBinary);
  await run("tar", ["-a", "-cf", archive, basename(archiveBinary)], {
    cwd: binaryDirectory,
  });
  await writeFile(
    resolve(releaseDirectory, "checksums.txt"),
    `${await fileSha256(archive)}  ${basename(archive)}\n`,
  );

  const result = await run(
    "powershell",
    [
      "-NoProfile",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      script,
      "-Version",
      options.version,
      "-InstallDir",
      installDirectory,
      "-Yes",
      "-NoPath",
      "-NoDeps",
    ],
    {
      allowFailure: true,
      env: {
        ...process.env,
        KENT_RELEASE_BASE: releaseBase,
        KENT_FAKE_MARKER: marker,
        KENT_FAKE_VERSION: "wrong",
      },
    },
  );
  if (result.code === 0) {
    throw new CommandError("installer recovery smoke unexpectedly succeeded");
  }
  const events = (await readFile(marker, "utf8"))
    .trim()
    .split("\n")
    .map((line) =>
      line.charCodeAt(line.length - 1) === 13 ? line.slice(0, -1) : line,
    );
  if (events.length !== 2 || events[0] !== "stopped" || events[1] !== "restarted") {
    throw new CommandError(
      `installer recovery smoke expected stopped/restarted events, got ${JSON.stringify(events)}`,
    );
  }
}

const fakeKentSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func mark(value string) {
	path := os.Getenv("KENT_FAKE_MARKER")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(1)
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, value); err != nil {
		os.Exit(1)
	}
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv("KENT_FAKE_VERSION"))
		return
	}
	if len(args) >= 2 && args[0] == "service" && args[1] == "status" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(1)
		}
		status := map[string]any{
			"installed": true,
			"loaded": true,
			"running": true,
			"command": []string{executable},
			"endpoint": "",
			"logs": []string{},
		}
		if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(args) >= 2 && args[0] == "service" && args[1] == "stop" {
		mark("stopped")
		return
	}
	if len(args) >= 3 && args[0] == "service" && args[1] == "restart" && args[2] == "--if-installed" {
		mark("restarted")
		return
	}
	fmt.Fprintf(os.Stderr, "unexpected args: %v\\n", args)
	os.Exit(1)
}
`;

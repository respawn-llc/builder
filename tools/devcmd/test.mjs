import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  rmdir,
  stat,
  writeFile,
} from "node:fs/promises";
import { cpus, homedir, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import {
  capture,
  CommandError,
  hostPlatform,
  main,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
} from "./runtime.mjs";
import { assertWorkspacePrepared } from "./dependencies.mjs";
import { serverPackagePatterns } from "./server-go-area.mjs";

await main(async () => {
  const [operation, ...args] = process.argv.slice(2);
  if (operation === "desktop") {
    await assertWorkspacePrepared("apps");
    await requireExecutable("pnpm");
    await run("pnpm", [
      "--dir",
      "apps/desktop",
      "exec",
      "vitest",
      "--config",
      "tooling/vite.config.ts",
      "run",
      ...withoutSeparator(args),
    ]);
  } else if (operation === "tui") {
    await requireExecutable("go");
    await run("go", ["test", "./cli/tui/...", ...withoutSeparator(args)]);
  } else if (operation === "server") {
    await server(args);
  } else {
    throw new CommandError("usage: test.mjs <server|desktop|tui>", 2);
  }
});

async function server(args) {
  await requireExecutable("go");
  const separator = args.indexOf("--");
  const launcherArgs = separator === -1 ? args : args.slice(0, separator);
  const goArgs = separator === -1 ? [] : args.slice(separator + 1);
  const { values } = parseArgs({
    args: launcherArgs,
    options: {
      timeout: { type: "string", default: "300" },
      workers: { type: "string", default: String(Math.min(cpus().length || 1, 10)) },
      "no-wall-clock-cap": { type: "boolean", default: false },
      "inherit-env": { type: "boolean", default: false },
    },
  });
  const workers = positiveInteger(values.workers, "--workers");
  const timeout = positiveInteger(values.timeout, "--timeout");
  if (timeout > 300) {
    throw new CommandError("--timeout must be a positive integer from 1 through 300", 2);
  }

  const environment = { ...process.env };
  if (!values["inherit-env"]) {
    for (const name of Object.keys(environment)) {
      if (name.startsWith("KENT_")) delete environment[name];
    }
  }
  const ptyFixtures = goArgs.length === 0 ? await preparePtyFixtures() : undefined;
  if (ptyFixtures) Object.assign(environment, ptyFixtures.environment);
  const commandArgs =
    goArgs.length === 0
      ? [
          "run",
          "./tools/testshard",
          "--workers",
          String(workers),
          ...serverPackagePatterns,
        ]
      : ["test", "-json", "-p", String(workers), ...goArgs];
  let status;
  try {
    status = await runTestProcess("go", commandArgs, {
      env: environment,
      usesJsonEvents: goArgs.length === 0,
      timeoutMs: values["no-wall-clock-cap"] ? undefined : timeout * 1000,
    });
  } finally {
    if (ptyFixtures) await ptyFixtures.release();
  }
  if (status !== 0) throw new CommandError(`Go tests exited with status ${status}`, status);
}

async function runTestProcess(command, args, options) {
  const child = spawn(command, args, {
    cwd: repoRoot,
    env: options.env,
    shell: false,
    detached: !hostPlatform.windows,
    stdio: ["inherit", "pipe", "inherit"],
    windowsHide: true,
  });
  let pending = "";
  if (options.usesJsonEvents) {
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      pending += chunk;
      let newline = pending.indexOf("\n");
      while (newline !== -1) {
        renderLine(pending.slice(0, newline + 1));
        pending = pending.slice(newline + 1);
        newline = pending.indexOf("\n");
      }
    });
  } else {
    child.stdout.on("data", (chunk) => process.stdout.write(chunk));
  }

  let timedOut = false;
  let forwardedSignal;
  let termination;
  const handlers = new Map();
  const terminate = () => {
    termination ||= terminateTree(child);
    return termination;
  };
  for (const signal of ["SIGINT", "SIGTERM"]) {
    const handler = () => {
      forwardedSignal = signal;
      void terminate();
    };
    handlers.set(signal, handler);
    process.once(signal, handler);
  }
  const timer =
    options.timeoutMs === undefined
      ? undefined
      : setTimeout(() => {
          timedOut = true;
          void terminate();
        }, options.timeoutMs);
  const { code, signal } = await waitForExit(child);
  if (termination) await termination;
  if (timer) clearTimeout(timer);
  for (const [name, handler] of handlers) process.removeListener(name, handler);
  if (options.usesJsonEvents && pending) renderLine(pending);
  if (timedOut) {
    console.error(`test suite exceeded ${options.timeoutMs / 1000}s wall-clock cap`);
    return 1;
  }
  if (forwardedSignal) return forwardedSignal === "SIGINT" ? 130 : 143;
  if (signal) return signal === "SIGINT" ? 130 : 143;
  return code ?? 1;
}

function renderLine(line) {
  try {
    const event = JSON.parse(line);
    if (
      event !== null &&
      typeof event === "object" &&
      typeof event.Output === "string"
    ) {
      process.stdout.write(event.Output);
    } else if (
      event !== null &&
      typeof event === "object" &&
      typeof event.Action === "string" &&
      typeof event.Package === "string"
    ) {
      return;
    } else {
      process.stdout.write(line);
    }
  } catch {
    process.stdout.write(line);
  }
}

function waitForExit(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve({ code: child.exitCode, signal: child.signalCode });
  }
  return new Promise((resolveExit, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => resolveExit({ code, signal }));
  });
}

async function terminateTree(child) {
  const pid = child.pid;
  if (!pid) return;
  if (hostPlatform.windows) {
    await taskkill(pid, false);
    await delay(2000);
    await taskkill(pid, true);
    return;
  }
  signalProcessGroup(pid, "SIGTERM");
  await delay(2000);
  signalProcessGroup(pid, "SIGKILL");
}

function signalProcessGroup(pid, signal) {
  try {
    process.kill(-pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}

async function taskkill(pid, force) {
  await new Promise((resolveExit) => {
    const child = spawn(
      "taskkill",
      ["/PID", String(pid), "/T", ...(force ? ["/F"] : [])],
      {
        shell: false,
        stdio: "ignore",
        windowsHide: true,
      },
    );
    child.once("error", resolveExit);
    child.once("exit", resolveExit);
  });
}

async function preparePtyFixtures() {
  const cacheRoot = resolve(systemCacheRoot(), "kent/test-fixtures");
  const leasesRoot = resolve(cacheRoot, "leases");
  const identity = [
    await capture("go", ["version"]),
    await capture("go", ["env", "GOOS", "GOARCH", "CGO_ENABLED"]),
    await capture("go", [
      "list",
      "-test",
      "-deps",
      "-export",
      "-f",
      "{{.ImportPath}}\t{{.BuildID}}",
      "core/cli/app",
      "core/cli/kent",
      "core/internal/testharness/pty/testdata/cmd/ansi-writer",
      "core/internal/testharness/pty/testdata/cmd/phase-input-writer",
      "core/internal/testharness/pty/testdata/cmd/phase-writer",
    ]),
  ].join("");
  const version = await readFile(resolve(repoRoot, "VERSION"), "utf8");
  const key = createHash("sha256").update(identity).update(version).digest("hex");
  const directory = resolve(cacheRoot, key);
  const complete = resolve(directory, "complete");
  const leaseParent = resolve(leasesRoot, key);
  const lease = resolve(leaseParent, randomUUID());
  await mkdir(lease, { recursive: true });
  try {
    try {
      await readFile(complete);
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      await mkdir(cacheRoot, { recursive: true });
      const staging = resolve(tmpdir(), `kent-pty-fixtures-${process.pid}-${Date.now()}`);
      await mkdir(staging);
      try {
        await run("go", [
          "test",
          "-c",
          "-o",
          join(staging, "kent-pty-fixture.test"),
          "core/cli/app",
        ]);
        await run("go", [
          "run",
          "./tools/devcmd/gobuild",
          "--output",
          join(staging, "kent"),
        ]);
        for (const [name, packagePath] of [
          ["ansi-writer", "core/internal/testharness/pty/testdata/cmd/ansi-writer"],
          ["phase-input-writer", "core/internal/testharness/pty/testdata/cmd/phase-input-writer"],
          ["phase-writer", "core/internal/testharness/pty/testdata/cmd/phase-writer"],
        ]) {
          await run("go", ["build", "-o", join(staging, name), packagePath]);
        }
        await writeFile(join(staging, "complete"), "");
        await rename(staging, directory).catch(async (renameError) => {
          if (renameError.code !== "EEXIST" && renameError.code !== "ENOTEMPTY") {
            throw renameError;
          }
        });
      } finally {
        await rm(staging, { recursive: true, force: true });
      }
    }
    await prunePtyFixtureCache(cacheRoot, leasesRoot, key);
  } catch (error) {
    await releasePtyFixtureLease(lease, leaseParent);
    throw error;
  }
  return {
    environment: {
      KENT_PTY_FIXTURE_BINARY: join(directory, "kent-pty-fixture.test"),
      KENT_PTY_KENT_BINARY: join(directory, "kent"),
      KENT_PTY_ANSI_WRITER_BINARY: join(directory, "ansi-writer"),
      KENT_PTY_PHASE_INPUT_WRITER_BINARY: join(directory, "phase-input-writer"),
      KENT_PTY_PHASE_WRITER_BINARY: join(directory, "phase-writer"),
    },
    release: () => releasePtyFixtureLease(lease, leaseParent),
  };
}

async function prunePtyFixtureCache(cacheRoot, leasesRoot, currentKey) {
  let entries;
  try {
    entries = await readdir(cacheRoot, { withFileTypes: true });
  } catch (error) {
    if (error.code === "ENOENT") return;
    throw error;
  }
  const generations = await Promise.all(
    entries
      .filter(
        (entry) =>
          entry.isDirectory() &&
          entry.name !== currentKey &&
          resolve(cacheRoot, entry.name) !== leasesRoot,
      )
      .map(async (entry) => ({
        key: entry.name,
        path: resolve(cacheRoot, entry.name),
        modified: (await stat(resolve(cacheRoot, entry.name))).mtimeMs,
      })),
  );
  generations.sort((left, right) => right.modified - left.modified);
  let retained = 0;
  for (const generation of generations) {
    if (await hasPtyFixtureLease(leasesRoot, generation.key)) continue;
    retained += 1;
    if (retained <= 7) continue;
    await rm(generation.path, { recursive: true, force: true });
  }
}

async function hasPtyFixtureLease(leasesRoot, key) {
  try {
    return (await readdir(resolve(leasesRoot, key))).length > 0;
  } catch (error) {
    if (error.code === "ENOENT") return false;
    throw error;
  }
}

async function releasePtyFixtureLease(lease, parent) {
  await rm(lease, { recursive: true, force: true });
  await rmdir(parent).catch((error) => {
    if (error.code !== "ENOENT" && error.code !== "ENOTEMPTY") throw error;
  });
}

function systemCacheRoot() {
  if (process.env.XDG_CACHE_HOME) return process.env.XDG_CACHE_HOME;
  if (hostPlatform.darwin) return resolve(homedir(), "Library/Caches");
  if (hostPlatform.windows) {
    return process.env.LOCALAPPDATA || resolve(homedir(), "AppData/Local");
  }
  return resolve(homedir(), ".cache");
}

function positiveInteger(value, name) {
  const number = Number(value);
  if (!Number.isInteger(number) || number <= 0 || String(number) !== value) {
    throw new CommandError(`${name} must be a positive integer`, 2);
  }
  return number;
}

function withoutSeparator(args) {
  return args[0] === "--" ? args.slice(1) : args;
}

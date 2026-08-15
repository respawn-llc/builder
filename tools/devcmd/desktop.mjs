import { spawn } from "node:child_process";
import { access, cp, mkdir, mkdtemp, open, readFile, readdir, rename, rm } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { dirname, extname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";
import {
  capture,
  CommandError,
  findExecutable,
  hostPlatform,
  main,
  parseArgs,
  readVersion,
  repoRoot,
  requireExecutable,
  run,
  normalizeVersion,
} from "./runtime.mjs";
import { assertWorkspacePrepared } from "./dependencies.mjs";

export async function buildDesktop(options = {}) {
  const { args = [], install = false, requireUpdaterKey = false } = options;
  if (install && !hostPlatform.darwin) {
    throw new CommandError(
      `Desktop installation tooling has not been built for ${hostPlatform.name}.`,
      2,
    );
  }
  if (install && args.length > 0) {
    throw new CommandError("Desktop installation does not accept Tauri build arguments.", 2);
  }
  await assertWorkspacePrepared("apps");
  await requireExecutable("pnpm");
  await requireExecutable("cargo");

  const version = normalizeVersion(
    options.version || process.env.KENT_VERSION || (await readVersion()),
  );
  if (!version) throw new CommandError("Unable to resolve the desktop version.");
  await compileAppIcon();

  const tauriArgs = [...args];
  if (hostPlatform.darwin && !requireUpdaterKey && !selectsBundles(tauriArgs)) {
    tauriArgs.unshift("--bundles", "app");
  }

  const environment = { ...process.env };
  if (environment.CI === "1") environment.CI = "true";
  if (environment.CI === "0") environment.CI = "false";
  const localKey = resolve(repoRoot, ".tauri/kent-desktop-updater.key");
  let createUpdaterArtifacts = true;
  if (!environment.TAURI_SIGNING_PRIVATE_KEY) {
    try {
      environment.TAURI_SIGNING_PRIVATE_KEY = await readFile(localKey, "utf8");
      environment.TAURI_SIGNING_PRIVATE_KEY_PASSWORD ||= "";
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      if (requireUpdaterKey) {
        throw new CommandError(
          "Updater signing key missing. Set TAURI_SIGNING_PRIVATE_KEY or add .tauri/kent-desktop-updater.key.",
          2,
        );
      }
      createUpdaterArtifacts = false;
    }
  }

  const configuration = JSON.parse(
    await readFile(resolve(repoRoot, "apps/desktop/src-tauri/tauri.conf.json"), "utf8"),
  );
  const icons = [...configuration.bundle.icon];
  if (await exists(resolve(repoRoot, "apps/desktop/src-tauri/icons/Assets.car"))) {
    icons.push("icons/Assets.car");
  }
  const buildConfiguration = JSON.stringify({
    version,
    bundle: { icon: icons, createUpdaterArtifacts },
  });
  await run(
    "pnpm",
    [
      "--dir",
      "apps/desktop",
      "exec",
      "tauri",
      "build",
      "--config",
      buildConfiguration,
      ...tauriArgs,
    ],
    { env: environment },
  );

  if (hostPlatform.darwin) {
    const profile = tauriArgs.some((argument) => argument === "--debug" || argument === "-d")
      ? "debug"
      : "release";
    const app = resolve(
      repoRoot,
      `apps/desktop/src-tauri/target/${profile}/bundle/macos/Kent.app`,
    );
    if (!process.env.APPLE_SIGNING_IDENTITY && (await exists(app))) {
      await run("codesign", [
        "--force",
        "--sign",
        "-",
        "--entitlements",
        "apps/desktop/src-tauri/entitlements.plist",
        app,
      ]);
    }
    if (install) await installMacApp(app);
  }
}

const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : undefined;
if (invokedPath === import.meta.url) {
  await main(async () => {
    const [operation, ...rawArgs] = process.argv.slice(2);
    if (operation === "web-build") return webBuild();
    if (operation === "dev") return browserDev(rawArgs);
    if (operation === "native-dev-web") return nativeDevWeb(rawArgs);
    if (operation === "smoke-macos") return smokeMacos(rawArgs);
    if (!["build", "install"].includes(operation)) {
      throw new CommandError("usage: desktop.mjs <build|install|dev|web-build>", 2);
    }
    const { values, positionals } = parseArgs({
      args: rawArgs,
      allowPositionals: true,
      options: {
        version: { type: "string" },
        "require-updater-key": { type: "boolean", default: false },
      },
    });
    await buildDesktop({
      version: values.version,
      requireUpdaterKey: values["require-updater-key"],
      install: operation === "install",
      args: positionals,
    });
  });
}

async function webBuild() {
  await assertWorkspacePrepared("apps");
  await requireExecutable("pnpm");
  await run("node", [
    "node_modules/typescript7/bin/tsc",
    "-b",
    "--pretty",
    "false",
  ], { cwd: resolve(repoRoot, "apps/desktop") });
  await run("pnpm", [
    "--dir",
    "apps/desktop",
    "exec",
    "vite",
    "--config",
    "tooling/vite.config.ts",
    "build",
  ]);
  await run("node", ["apps/scripts/check-desktop-font-assets.mjs"]);
}

async function browserDev(args) {
  await assertWorkspacePrepared("apps");
  await requireExecutable("pnpm");
  await run("pnpm", [
    "--dir",
    "apps/desktop",
    "exec",
    "vite",
    "--config",
    "tooling/vite.config.ts",
    "--host",
    "127.0.0.1",
    "--open",
    "/",
    ...args,
  ]);
}

async function nativeDevWeb(args) {
  await assertWorkspacePrepared("apps");
  await requireExecutable("pnpm");
  await run("pnpm", [
    "--dir",
    "apps/desktop",
    "exec",
    "vite",
    "--config",
    "tooling/vite.config.ts",
    "--host",
    "127.0.0.1",
    ...args,
  ]);
}

async function compileAppIcon() {
  if (!hostPlatform.darwin) return;
  const icon = resolve(repoRoot, "apps/desktop/src-tauri/icons/Kent.icon");
  const output = resolve(repoRoot, "apps/desktop/src-tauri/icons/Assets.car");
  const actool = await findExecutable("actool");
  if (!(await exists(icon))) {
    await rm(output, { force: true });
    return;
  }
  const xcodeSelect = await findExecutable("xcode-select");
  const developerDirectory = xcodeSelect
    ? (
        await capture(xcodeSelect, ["--print-path"], {
          allowFailure: true,
          stderr: "ignore",
        })
      ).trim()
    : "";
  const iconComposer = developerDirectory
    ? resolve(dirname(developerDirectory), "Applications/Icon Composer.app")
    : undefined;
  if (!actool || !iconComposer || !(await exists(iconComposer))) {
    await rm(output, { force: true });
    console.error(
      "Icon Composer unavailable; skipping liquid-glass app icon. Tauri will fall back to PNG -> icns.",
    );
    return;
  }

  const temporary = await mkdtemp(join(tmpdir(), "kent-icon-"));
  try {
    const temporaryIcon = join(temporary, "Icon.icon");
    await cp(icon, temporaryIcon, { recursive: true });
    for (let attempt = 1; attempt <= 3; attempt++) {
      const killall = await findExecutable("killall");
      if (killall) await run(killall, ["-9", "ibtoold"], { allowFailure: true });
      const attemptOutput = join(temporary, `out_${attempt}`);
      await mkdir(attemptOutput);
      const result = await run(
        actool,
        [
          temporaryIcon,
          "--compile",
          attemptOutput,
          "--output-format",
          "human-readable-text",
          "--notices",
          "--warnings",
          "--output-partial-info-plist",
          join(attemptOutput, "assetcatalog_generated_info.plist"),
          "--app-icon",
          "Icon",
          "--include-all-app-icons",
          "--accent-color",
          "AccentColor",
          "--enable-on-demand-resources",
          "NO",
          "--development-region",
          "en",
          "--target-device",
          "mac",
          "--minimum-deployment-target",
          "15.0",
          "--platform",
          "macosx",
        ],
        { allowFailure: true, stdio: "ignore" },
      );
      const built = join(attemptOutput, "Assets.car");
      if (result.code === 0 && (await exists(built))) {
        await cp(built, output);
        return;
      }
    }
    throw new CommandError("Failed to compile the macOS app icon after 3 attempts.");
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

async function installMacApp(app) {
  if (!(await exists(app))) throw new CommandError(`Built macOS app bundle not found: ${app}`);
  const destination = "/Applications/Kent.app";
  const trash = join(homedir(), ".Trash");
  await mkdir(trash, { recursive: true });
  const previous = join(trash, `Kent.app.previous.${Date.now()}`);
  let movedPrevious = false;
  if (await exists(destination)) {
    await rename(destination, previous);
    movedPrevious = true;
  }
  try {
    await run("ditto", [app, destination]);
  } catch (error) {
    if (await exists(destination)) {
      await rename(destination, join(trash, `Kent.app.failed.${Date.now()}`));
    }
    if (movedPrevious) await rename(previous, destination);
    throw error;
  }
}

async function smokeMacos(args) {
  if (!hostPlatform.darwin) {
    throw new CommandError("The macOS desktop smoke tooling has not been built for this host.", 2);
  }
  const { values } = parseArgs({
    args,
    options: { "bundle-dir": { type: "string" } },
  });
  if (!values["bundle-dir"]) throw new CommandError("--bundle-dir is required", 2);
  const bundleDirectory = resolve(repoRoot, values["bundle-dir"]);
  const dmgs = (await readdir(bundleDirectory))
    .filter((name) => extname(name) === ".dmg")
    .map((name) => resolve(bundleDirectory, name));
  if (dmgs.length !== 1) {
    throw new CommandError(`Expected exactly one macOS DMG, found ${dmgs.length}.`, 2);
  }

  const mountDirectory = await mkdtemp(join(tmpdir(), "kent-desktop-dmg-"));
  let mounted = false;
  let detached = false;
  let appProcess;
  let appLog;
  let logPath;
  let interruptedSignal;
  let operationError;
  const signalHandlers = new Map();
  try {
    await run("hdiutil", [
      "attach",
      dmgs[0],
      "-nobrowse",
      "-readonly",
      "-mountpoint",
      mountDirectory,
    ], { input: "Y\n" });
    mounted = true;
    const app = join(mountDirectory, "Kent.app");
    const executable = join(app, "Contents/MacOS/kent-desktop");
    const minimumVersion = (
      await capture("plutil", [
        "-extract",
        "LSMinimumSystemVersion",
        "raw",
        join(app, "Contents/Info.plist"),
      ])
    ).trim();
    if (minimumVersion !== "15.0") {
      throw new CommandError(`Expected LSMinimumSystemVersion 15.0, got ${minimumVersion}.`);
    }
    await run("lipo", [executable, "-verify_arch", "arm64"]);

    logPath = join(tmpdir(), `kent-desktop-${process.pid}.log`);
    appLog = await open(logPath, "w");
    appProcess = spawn(executable, [], {
      cwd: repoRoot,
      detached: true,
      shell: false,
      stdio: ["ignore", appLog.fd, appLog.fd],
    });
    for (const signal of ["SIGINT", "SIGTERM"]) {
      const handler = () => {
        interruptedSignal = signal;
        signalDesktop(appProcess, "SIGTERM");
      };
      signalHandlers.set(signal, handler);
      process.once(signal, handler);
    }
    const earlyExit = await Promise.race([
      waitForDesktopExit(appProcess),
      delay(10_000).then(() => undefined),
    ]);
    if (interruptedSignal) {
      throw new CommandError(
        `desktop smoke interrupted by ${interruptedSignal}`,
        interruptedSignal === "SIGINT" ? 130 : 143,
      );
    }
    if (earlyExit !== undefined) {
      throw new CommandError(
        `Kent desktop exited during smoke startup with status ${earlyExit.code ?? earlyExit.signal}.`,
      );
    }
    await terminateDesktopProcess(appProcess);
    if (interruptedSignal) {
      throw new CommandError(
        `desktop smoke interrupted by ${interruptedSignal}`,
        interruptedSignal === "SIGINT" ? 130 : 143,
      );
    }
  } catch (error) {
    operationError = error;
  } finally {
    for (const [signal, handler] of signalHandlers) process.removeListener(signal, handler);
    try {
      if (appProcess) await terminateDesktopProcess(appProcess);
    } catch (error) {
      operationError = combineErrors(operationError, error);
    }
    if (appLog) {
      try {
        await appLog.close();
      } catch (error) {
        operationError = combineErrors(operationError, error);
      }
      appLog = undefined;
    }
    if (operationError && logPath && (await exists(logPath))) {
      const output = await readFile(logPath, "utf8");
      if (output) console.error(output);
    }
    if (logPath) await rm(logPath, { force: true });
    if (mounted) {
      try {
        await run("hdiutil", ["detach", mountDirectory]);
        detached = true;
      } catch (error) {
        operationError = combineErrors(operationError, error);
      }
    }
    if (!mounted || detached) {
      await rm(mountDirectory, { recursive: true, force: true });
    }
  }
  if (operationError) throw operationError;
}

function signalDesktop(child, signal) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  try {
    process.kill(-child.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}

async function terminateDesktopProcess(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return waitForDesktopExit(child);
  }
  const exit = waitForDesktopExit(child);
  signalDesktop(child, "SIGTERM");
  const gracefulExit = await Promise.race([
    exit,
    delay(2_000).then(() => undefined),
  ]);
  if (gracefulExit !== undefined) return gracefulExit;
  signalDesktop(child, "SIGKILL");
  return exit;
}

function waitForDesktopExit(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve({ code: child.exitCode, signal: child.signalCode });
  }
  return new Promise((resolveExit, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => resolveExit({ code, signal }));
  });
}

function combineErrors(primary, cleanup) {
  if (!primary) return cleanup;
  return new CommandError(
    `${primary.message}\nCleanup failed: ${cleanup.message}`,
    primary instanceof CommandError ? primary.exitCode : 1,
  );
}

function selectsBundles(args) {
  return args.some(
    (argument) =>
      argument === "--bundles" || argument === "-b" || argument.startsWith("--bundles="),
  );
}

async function exists(path) {
  try {
    await access(path);
    return true;
  } catch {
    return false;
  }
}

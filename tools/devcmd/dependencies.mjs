import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import {
  CommandError,
  main,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
} from "./runtime.mjs";

const workspaces = {
  apps: resolve(repoRoot, "apps"),
  docs: resolve(repoRoot, "docs"),
};

export async function assertWorkspacePrepared(name) {
  const root = workspace(name);
  const sourceLock = await readFile(resolve(root, "pnpm-lock.yaml"));
  const [modules, installedLock] = await Promise.all([
    readOptional(resolve(root, "node_modules", ".modules.yaml")),
    readOptional(resolve(root, "node_modules", ".pnpm", "lock.yaml")),
  ]);
  if (!modules || !installedLock || !sourceLock.equals(installedLock)) {
    throw new CommandError(
      `${name} dependencies are missing or stale. Run \`just setup --apply\`.`,
      2,
    );
  }
}

export async function installWorkspace(name) {
  try {
    await assertWorkspacePrepared(name);
    return;
  } catch (error) {
    if (!(error instanceof CommandError)) throw error;
  }

  await requireExecutable("pnpm", "Install pnpm, then rerun `just setup --apply`.");
  await run("pnpm", ["--dir", workspace(name), "install", "--frozen-lockfile"], {
    env: { ...process.env, npm_config_confirm_modules_purge: "false" },
  });
  await assertWorkspacePrepared(name);
}

const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : undefined;
if (invokedPath === import.meta.url) {
  await main(async () => {
    const { positionals } = parseArgs({
      args: process.argv.slice(2),
      allowPositionals: true,
      options: {},
    });
    if (positionals.length === 0) throw new CommandError("missing dependency operation", 2);

    const [operation, target, ...extra] = positionals;
    if (extra.length > 0) throw new CommandError("unexpected dependency arguments", 2);
    if (operation === "install") {
      await install(target);
    } else if (operation === "update" && target === undefined) {
      await update();
    } else {
      throw new CommandError("usage: dependencies.mjs install <apps|docs|all> | update", 2);
    }
  });
}

async function install(target) {
  if (!["apps", "docs", "all"].includes(target)) {
    throw new CommandError("dependency install target must be apps, docs, or all", 2);
  }
  if (target !== "all") {
    await installWorkspace(target);
    return;
  }

  await requireExecutable("go");
  await requireExecutable("cargo");
  await run("go", ["mod", "download"]);
  await installWorkspace("apps");
  await installWorkspace("docs");
  await run("cargo", [
    "fetch",
    "--manifest-path",
    "apps/desktop/src-tauri/Cargo.toml",
    "--locked",
  ]);
}

async function update() {
  for (const executable of ["go", "pnpm", "cargo"]) await requireExecutable(executable);
  await run("go", ["get", "-u", "-t", "./..."]);
  await run("go", ["mod", "tidy"]);
  await run("pnpm", [
    "--dir",
    workspaces.apps,
    "--recursive",
    "--include-workspace-root",
    "up",
    "--latest",
  ]);
  await run("pnpm", [
    "--dir",
    workspaces.apps,
    "--filter",
    "@app/desktop",
    "--filter",
    "@app/native-bridge",
    "add",
    "--save-dev",
    "typescript@^6.0.3",
  ]);
  await run("pnpm", ["--dir", workspaces.docs, "up", "--latest"]);
  await run("cargo", [
    "update",
    "--manifest-path",
    "apps/desktop/src-tauri/Cargo.toml",
  ]);
}

function workspace(name) {
  const root = workspaces[name];
  if (!root) throw new CommandError(`unknown pnpm workspace: ${name}`, 2);
  return root;
}

async function readOptional(path) {
  try {
    return await readFile(path);
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    throw error;
  }
}

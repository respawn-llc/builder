import { readdir } from "node:fs/promises";
import { extname, resolve } from "node:path";
import {
  capture,
  CommandError,
  main,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
} from "./runtime.mjs";
import { assertWorkspacePrepared } from "./dependencies.mjs";
import {
  serverGoDirectories,
  serverPackagePatterns,
} from "./server-go-area.mjs";

await main(async () => {
  const [area, ...args] = process.argv.slice(2);
  const { values } = parseArgs({
    args,
    options: { "dry-run": { type: "boolean", default: false } },
  });
  if (area === "server") await lintServer(values["dry-run"]);
  else if (area === "desktop") await lintDesktop(values["dry-run"]);
  else if (area === "tui") await lintTui(values["dry-run"]);
  else if (area === "docs") await lintDocs(values["dry-run"]);
  else throw new CommandError("usage: lint.mjs <server|desktop|tui|docs> [--dry-run]", 2);
});

async function lintServer(dryRun) {
  await requireExecutables(["go", "gofmt"]);
  await formatGoDirectories(serverGoDirectories, dryRun);
  await run("go", ["run", "github.com/bufbuild/buf/cmd/buf@v1.72.0", "lint"]);
  await run("node", ["tools/devcmd/generate.mjs", "check", "--kind", "go"]);
  await run("go", ["vet", ...serverPackagePatterns]);
  await run("go", ["run", "./cmd/architectureguard", "."]);
}

async function lintTui(dryRun) {
  await requireExecutables(["go", "gofmt"]);
  await formatGoDirectories(["cli/tui"], dryRun);
  await run("go", ["vet", "./cli/tui/..."]);
}

async function lintDesktop(dryRun) {
  await assertWorkspacePrepared("apps");
  await requireExecutable("pnpm");
  await run("node", ["tools/devcmd/generate.mjs", "check", "--kind", "typescript"]);
  await run("node", ["apps/scripts/check-dependency-policy.mjs"]);
  await run("node", ["apps/scripts/check-typescript-policy.mjs"]);
  await run("node", ["--test", "apps/scripts/check-eslint-architecture.test.mjs"]);
  await run("pnpm", [
    "--dir",
    "apps",
    "exec",
    "prettier",
    dryRun ? "--check" : "--write",
    ".",
  ]);
  await run("pnpm", [
    "--dir",
    "apps/desktop",
    "exec",
    "eslint",
    ".",
    ...(dryRun ? [] : ["--fix"]),
  ]);
  await run(
    "node",
    ["node_modules/typescript7/bin/tsc", "-b", "--pretty", "false"],
    { cwd: resolve(repoRoot, "apps/desktop") },
  );
}

async function lintDocs(dryRun) {
  await assertWorkspacePrepared("docs");
  await requireExecutable("pnpm");
  await run("pnpm", [
    "--dir",
    "docs",
    "exec",
    "prettier",
    dryRun ? "--check" : "--write",
    ".",
  ]);
  await run("pnpm", ["--dir", "docs", "exec", "astro", "check"]);
}

async function requireExecutables(names) {
  for (const name of names) await requireExecutable(name);
}

async function formatGoDirectories(directories, dryRun) {
  const files = (
    await Promise.all(
      directories.map((directory) => goFiles(resolve(repoRoot, directory))),
    )
  ).flat();
  if (dryRun) {
    const output = await capture("gofmt", ["-l", ...files]);
    if (output.trim()) throw new CommandError(`Go formatting required:\n${output.trim()}`);
  } else {
    await run("gofmt", ["-w", ...files]);
  }
}

async function goFiles(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.name === ".git" || entry.name === "node_modules" || entry.name === "target") continue;
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) result.push(...(await goFiles(path)));
    else if (entry.isFile() && extname(entry.name) === ".go") result.push(path);
  }
  return result;
}

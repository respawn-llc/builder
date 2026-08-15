import { main, parseArgs, requireExecutable, run } from "./runtime.mjs";
import { assertWorkspacePrepared } from "./dependencies.mjs";

await main(async () => {
  const { positionals } = parseArgs({
    args: process.argv.slice(2),
    allowPositionals: true,
    options: {},
  });
  const [operation, ...args] = positionals;
  await assertWorkspacePrepared("docs");
  await requireExecutable("pnpm");

  if (operation === "build") {
    await run("pnpm", ["--dir", "docs", "exec", "astro", "build", ...args]);
    await run("node", ["docs/scripts/emit-deployment-files.mjs"]);
  } else if (operation === "dev") {
    await run("pnpm", ["--dir", "docs", "exec", "astro", "dev", ...args]);
  } else if (operation === "test" && args.length === 0) {
    await run("node", ["--test", "docs/scripts/sync-mirrored-docs.test.mjs"]);
  } else {
    throw new Error("usage: docs.mjs <build|dev|test>");
  }
});

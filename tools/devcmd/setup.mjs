import {
  CommandError,
  main,
  parseArgs,
  requireExecutable,
  run,
} from "./runtime.mjs";

await main(async () => {
  const { values } = parseArgs({
    args: process.argv.slice(2),
    options: {
      apply: { type: "boolean", default: false },
      just: { type: "string" },
    },
  });
  if (!values.just) throw new CommandError("--just is required", 2);

  const prerequisites = {};
  for (const executable of ["go", "pnpm", "cargo", "git"]) {
    prerequisites[executable] = await requireExecutable(executable);
  }

  console.log("Kent checkout setup plan:");
  for (const [name, path] of Object.entries(prerequisites)) {
    console.log(`  validate ${name}: ${path}`);
  }
  console.log("  download Go modules");
  console.log("  install apps dependencies from the frozen lockfile");
  console.log("  install docs dependencies from the frozen lockfile");
  console.log("  fetch active desktop Cargo dependencies");
  console.log("  configure core.hooksPath=.githooks");

  if (!values.apply) {
    console.log("Dry run only. Run `just setup --apply` to apply this plan.");
    return;
  }

  await run(values.just, ["install", "_dependencies"]);
  await run("git", ["config", "--local", "core.hooksPath", ".githooks"]);
});

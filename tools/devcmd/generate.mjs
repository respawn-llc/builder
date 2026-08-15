import { readdir, readFile, writeFile } from "node:fs/promises";
import { relative, resolve } from "node:path";
import {
  CommandError,
  main,
  parseArgs,
  repoRoot,
  requireExecutable,
  run,
  withTemporaryDirectory,
} from "./runtime.mjs";

const outputs = [
  {
    template: "buf.gen.go.yaml",
    directory: "shared/protoapi/gen",
    kind: "go",
  },
  {
    template: "buf.gen.ts.yaml",
    directory: "apps/desktop/packages/server-api-contract/src/gen",
    kind: "typescript",
  },
];

await main(async () => {
  const { values, positionals } = parseArgs({
    args: process.argv.slice(2),
    allowPositionals: true,
    options: {
      "typescript-only": { type: "boolean", default: false },
      kind: { type: "string", multiple: true, default: [] },
    },
  });

  if (positionals.length !== 1 || !["write", "check"].includes(positionals[0])) {
    throw new CommandError("usage: generate.mjs <write|check>", 2);
  }

  const selectedOutputs = selectOutputs(values);
  await requireExecutable("go");
  if (positionals[0] === "write") {
    for (const output of selectedOutputs) await generate(output);
    return;
  }

  await withTemporaryDirectory("kent-generated-", async (temporaryDirectory) => {
    for (const output of selectedOutputs) {
      await generate(output, temporaryDirectory);
      await assertTreesEqual(
        resolve(repoRoot, output.directory),
        resolve(temporaryDirectory, output.directory),
      );
    }
  });
});

function selectOutputs(values) {
  if (values["typescript-only"] && values.kind.length > 0) {
    throw new CommandError("--typescript-only cannot be combined with --kind", 2);
  }

  const kinds = values["typescript-only"] ? ["typescript"] : values.kind;
  if (kinds.length === 0) return outputs;

  for (const kind of kinds) {
    if (!outputs.some((output) => output.kind === kind)) {
      throw new CommandError(`unknown generated-output kind: ${kind}`, 2);
    }
  }
  return outputs.filter((output) => kinds.includes(output.kind));
}

async function generate(output, baseDirectory) {
  const args = [
    "run",
    "github.com/bufbuild/buf/cmd/buf@v1.72.0",
    "generate",
    "--template",
    output.template,
  ];
  if (baseDirectory) args.push("--output", baseDirectory);
  await run("go", args);
  await normalizeTrailingNewline(
    resolve(baseDirectory || repoRoot, output.directory),
  );
}

async function normalizeTrailingNewline(root) {
  await visit(root);

  async function visit(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = resolve(directory, entry.name);
      if (entry.isDirectory()) {
        await visit(path);
        continue;
      }
      if (!entry.isFile()) continue;
      const data = await readFile(path);
      let contentEnd = data.length;
      while (
        contentEnd > 0 &&
        (data[contentEnd - 1] === 10 || data[contentEnd - 1] === 13)
      ) {
        contentEnd--;
      }
      if (contentEnd === data.length - 1 && data[data.length - 1] === 10) {
        continue;
      }
      await writeFile(
        path,
        Buffer.concat([data.subarray(0, contentEnd), Buffer.from("\n")]),
      );
    }
  }
}

async function assertTreesEqual(committedRoot, generatedRoot) {
  const [committed, generated] = await Promise.all([
    readTree(committedRoot),
    readTree(generatedRoot),
  ]);
  const paths = [...new Set([...committed.keys(), ...generated.keys()])].sort();
  const differences = [];

  for (const path of paths) {
    if (!committed.has(path)) {
      differences.push(`missing committed file: ${path}`);
    } else if (!generated.has(path)) {
      differences.push(`stale committed file: ${path}`);
    } else if (!committed.get(path).equals(generated.get(path))) {
      differences.push(`changed generated file: ${path}`);
    }
  }

  if (differences.length > 0) {
    throw new CommandError(
      `Generated output is stale. Run \`just gen\`.\n${differences.join("\n")}`,
    );
  }
}

async function readTree(root) {
  const result = new Map();
  await visit(root);
  return result;

  async function visit(directory) {
    let entries;
    try {
      entries = await readdir(directory, { withFileTypes: true });
    } catch (error) {
      if (error.code === "ENOENT") return;
      throw error;
    }

    entries.sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const absolutePath = resolve(directory, entry.name);
      if (entry.isDirectory()) {
        await visit(absolutePath);
      } else if (entry.isFile()) {
        result.set(relative(root, absolutePath), await readFile(absolutePath));
      }
    }
  }
}

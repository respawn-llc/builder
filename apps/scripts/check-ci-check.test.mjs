import { execFile } from "node:child_process";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const execFileAsync = promisify(execFile);
const repoRoot = fileURLToPath(new URL("../../", import.meta.url));

test(
  "frontend lint installs frozen workspace dependencies first",
  { skip: process.platform === "win32" },
  async (context) => {
    const fixtureRoot = await mkdtemp(join(tmpdir(), "kent-ci-check-"));
    context.after(async () => {
      await rm(fixtureRoot, { recursive: true, force: true });
    });
    const callLog = join(fixtureRoot, "pnpm-calls.jsonl");
    const pnpmStub = join(fixtureRoot, "pnpm");
    await writeFile(
      pnpmStub,
      `#!/usr/bin/env node
import { appendFileSync } from "node:fs";
appendFileSync(process.env.PNPM_CALL_LOG, JSON.stringify(process.argv.slice(2)) + "\\n");
`,
    );
    await chmod(pnpmStub, 0o755);

    await execFileAsync("bash", [join(repoRoot, "scripts/ci-check.sh"), "frontend-lint"], {
      cwd: repoRoot,
      env: {
        ...process.env,
        PATH: `${fixtureRoot}${delimiter}${process.env.PATH ?? ""}`,
        PNPM_CALL_LOG: callLog,
      },
    });

    const calls = (await readFile(callLog, "utf8"))
      .trimEnd()
      .split("\n")
      .map((line) => JSON.parse(line));
    assert.deepEqual(calls, [
      ["--dir", "apps", "install", "--frozen-lockfile"],
      ["--dir", "apps", "lint"],
    ]);
  },
);

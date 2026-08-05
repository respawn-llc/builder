import assert from "node:assert/strict";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { checkDependencyPolicy } from "./check-dependency-policy.mjs";

test("accepts package deps that match policy and workspace gates", async () => {
  const workspaceRoot = await makeWorkspace({
    workspaceConfig: "packages:\n  - app\nminimumReleaseAge: 10080\nonlyBuiltDependencies: []\n",
    policy: {
      minimumReleaseAgeMinutes: 10080,
      minimumReleaseAgeExclude: [],
      directDependencyAllowlist: {
        app: {
          dependencies: ["react"],
          devDependencies: ["typescript"],
        },
      },
    },
    packages: [
      {
        dir: "app",
        json: {
          name: "app",
          dependencies: { react: "^19.0.0" },
          devDependencies: { typescript: "^6.0.0" },
        },
      },
    ],
  });

  assert.deepEqual(await checkDependencyPolicy(workspaceRoot), []);
});

test("rejects unreviewed direct deps", async () => {
  const workspaceRoot = await makeWorkspace({
    workspaceConfig: "packages:\n  - app\nminimumReleaseAge: 10080\nonlyBuiltDependencies: []\n",
    policy: {
      minimumReleaseAgeMinutes: 10080,
      minimumReleaseAgeExclude: [],
      directDependencyAllowlist: {
        app: {
          dependencies: ["react"],
        },
      },
    },
    packages: [
      {
        dir: "app",
        json: {
          name: "app",
          dependencies: { lodash: "^5.0.0", react: "^19.0.0" },
        },
      },
    ],
  });

  assert.deepEqual(await checkDependencyPolicy(workspaceRoot), [
    "app dependencies contains unreviewed dependency lodash.",
  ]);
});

test("rejects workspace policy drift", async () => {
  const workspaceRoot = await makeWorkspace({
    workspaceConfig: "packages:\n  - app\nminimumReleaseAge: 60\n",
    policy: {
      minimumReleaseAgeMinutes: 10080,
      minimumReleaseAgeExclude: [],
      directDependencyAllowlist: {
        app: {},
      },
    },
    packages: [
      {
        dir: "app",
        json: {
          name: "app",
        },
      },
    ],
  });

  assert.deepEqual(await checkDependencyPolicy(workspaceRoot), [
    "pnpm-workspace.yaml must set minimumReleaseAge: 10080 to enforce dependency maturity.",
    "pnpm-workspace.yaml must set onlyBuiltDependencies: [] so install scripts need explicit review.",
  ]);
});

test("rejects an approved release-age exclusion missing from workspace policy", async () => {
  const workspaceRoot = await makeWorkspace({
    workspaceConfig: "packages:\n  - app\nminimumReleaseAge: 10080\nonlyBuiltDependencies: []\n",
    policy: {
      minimumReleaseAgeMinutes: 10080,
      minimumReleaseAgeExclude: ["vite@8.2.0"],
      directDependencyAllowlist: {
        app: {},
      },
    },
    packages: [
      {
        dir: "app",
        json: {
          name: "app",
        },
      },
    ],
  });

  assert.equal((await checkDependencyPolicy(workspaceRoot)).length, 1);
});

async function makeWorkspace({ workspaceConfig, policy, packages }) {
  const workspaceRoot = await mkdtemp(join(tmpdir(), "app-dep-policy-"));
  await writeFile(join(workspaceRoot, "pnpm-workspace.yaml"), workspaceConfig);
  await writeFile(join(workspaceRoot, "dependency-policy.json"), JSON.stringify(policy));

  for (const pkg of packages) {
    const packageDir = join(workspaceRoot, pkg.dir);
    await mkdir(packageDir, { recursive: true });
    await writeFile(join(packageDir, "package.json"), JSON.stringify(pkg.json));
  }

  return workspaceRoot;
}

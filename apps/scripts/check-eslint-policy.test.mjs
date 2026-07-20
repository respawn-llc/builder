import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { createArchitecturePolicy } from "../desktop/eslint-architecture.config.js";
import eslintConfig from "../desktop/eslint.config.js";

const desktopRequire = createRequire(
  new URL("../desktop/package.json", import.meta.url),
);
const { ESLint } = desktopRequire("eslint");
const desktopRoot = fileURLToPath(new URL("../desktop", import.meta.url));
const desktopPolicyLinter = new ESLint({
  cwd: desktopRoot,
  overrideConfigFile: true,
  overrideConfig: eslintConfig,
});
const fixtureRoot = fileURLToPath(
  new URL("../desktop/eslint-fixtures/architecture", import.meta.url),
);

test("desktop ESLint config explicitly forbids explicit any", () => {
  const rule = findRule("@typescript-eslint/no-explicit-any");

  assert.equal(rule, "error");
});

test("desktop ESLint config explicitly enables type-aware async and unsafe-value rules", () => {
  assert.equal(findRule("@typescript-eslint/no-floating-promises"), "error");
  assert.equal(findRule("@typescript-eslint/no-unsafe-assignment"), "error");
  assert.equal(findRule("@typescript-eslint/no-unsafe-call"), "error");
  assert.equal(findRule("@typescript-eslint/no-unsafe-member-access"), "error");
  assert.equal(findRule("@typescript-eslint/promise-function-async"), "error");
  assert.equal(findRule("@typescript-eslint/require-await"), "off");
  assert.deepEqual(findRule("@typescript-eslint/return-await"), [
    "error",
    "in-try-catch",
  ]);
});

test("desktop ESLint config explicitly forbids unsafe type assertions", () => {
  assert.deepEqual(findRule("@typescript-eslint/consistent-type-assertions"), [
    "error",
    {
      assertionStyle: "never",
    },
  ]);
});

test("desktop ESLint config explicitly enforces GUI architecture rules", () => {
  assert.equal(findRule("app/no-array-index-key"), "error");
  assert.equal(findRule("app/no-eslint-disable"), "error");
  assert.equal(findRule("app/no-mutable-exports"), "error");
  assert.equal(findRule("app/no-typeof-type-guards"), "error");
  assert.equal(findRule("app/no-useeffect-data-loading"), "error");
});

test("desktop ESLint rule rejects every eslint-disable/eslint-enable directive form", async () => {
  const directives = [
    "/* eslint-disable */",
    "/* eslint-disable app/no-eslint-disable */",
    "/* eslint-disable max-lines -- fake reason */",
    "// eslint-disable-line no-console",
    "// eslint-disable-next-line complexity",
    "/* eslint-enable max-lines */",
  ];

  for (const directive of directives) {
    const messages = await lintWithAppArchitectureRules(
      `${directive}\nconst value = 1;\n`,
    );
    assert.ok(
      messages.some((message) => message.ruleId === "app/no-eslint-disable"),
      `expected app/no-eslint-disable to flag ${directive}`,
    );
  }
});

test("desktop ESLint rule ignores ordinary comments that merely mention eslint", async () => {
  const messages = await lintWithAppArchitectureRules(
    "// eslint runs in CI to enforce these rules\nconst value = 1;\n",
  );

  assert.ok(
    !messages.some((message) => message.ruleId === "app/no-eslint-disable"),
  );
});

test("desktop ESLint config explicitly enforces complexity and debug-output limits", () => {
  assert.deepEqual(findRule("complexity"), ["error", { max: 12 }]);
  assert.deepEqual(findRule("max-depth"), ["error", 4]);
  assert.deepEqual(findRuleForFiles("max-lines", "**/*.{ts,tsx}"), [
    "error",
    { max: 650, skipBlankLines: true, skipComments: true },
  ]);
  assert.deepEqual(findRuleForFiles("max-lines", "**/*.test.{ts,tsx}"), [
    "error",
    { max: 1100, skipBlankLines: true, skipComments: true },
  ]);
  assert.deepEqual(findRule("max-params"), ["error", 4]);
  assert.equal(findRule("no-console"), "error");
});

const dependencyDiagnostic = [{ ruleId: "boundaries/dependencies", line: 1 }];
const unknownDependencyDiagnostics = [
  ...dependencyDiagnostic,
  {
    ruleId: "boundaries/no-unknown-dependencies",
    line: 1,
  },
];

const architecturePolicyGroups = [
  policyGroup(
    "src/app/allowed-public-seams.ts",
    [
      "./index",
      "@/app-facade",
      "@/features/alpha",
      "@/shared/alpha",
      "@/ui",
      "@/api",
      "@/api/composition",
      "@/i18n",
      "@app/native-bridge",
      "@xyflow/react",
      "elkjs/lib/elk-api",
      "elkjs/lib/elk.bundled.js",
    ],
    [
      "@/app-facade/internal",
      "@/features/alpha/internal",
      "../../packages/native-bridge/src/internal",
      "@/shared/alpha/internal",
      "@/ui/internal",
      "../../src-tauri/tauri.conf.json",
    ],
  ),
  policyGroup(
    "src/app-facade/index.ts",
    ["@/api", "@/ui", "@app/native-bridge"],
    ["@/api/composition", "@/features/alpha", "@/app"],
  ),
  policyGroup(
    "src/features/alpha/index.ts",
    [
      "./internal",
      "@/app-facade",
      "@/shared/alpha",
      "@/ui",
      "@/api",
      "@/i18n",
      "@xyflow/react",
      "elkjs/lib/elk-api",
      "elkjs/lib/elk.bundled.js",
      "./styles.css",
    ],
    [
      "@/api/composition",
      "@/api/internal",
      "@/features/beta",
      "@app/native-bridge",
      "../../shared/alpha",
      "@/app",
      "@/vendor/xyflow-react-types",
      "@/test-support/harness",
    ],
  ),
  policyGroup(
    "src/shared/alpha/index.ts",
    [
      "./internal",
      "@/app-facade",
      "@/api",
      "@/ui",
      "@/shared/beta",
      "@xyflow/react",
      "elkjs/lib/elk-api",
      "elkjs/lib/elk.bundled.js",
    ],
    [
      "@/api/composition",
      "@/shared/beta/internal",
      "@/features/alpha",
      "@app/native-bridge",
      "../beta",
      "@/app",
    ],
  ),
  policyGroup("src/api/index.ts", ["./internal"], ["@/ui"]),
  policyGroup(
    "src/ui/index.ts",
    ["./internal"],
    ["@/api", "@/features/alpha", "@app/native-bridge", "@/app"],
  ),
  policyGroup(
    "src/test-support/harness/index.ts",
    [
      "@/app-facade",
      "@/features/alpha",
      "@/shared/alpha",
      "@/ui",
      "@/api",
      "@/api/composition",
      "@/i18n",
      "@app/native-bridge",
      "@xyflow/react",
      "elkjs/lib/elk-api",
      "elkjs/lib/elk.bundled.js",
    ],
    ["@/api/internal", "@/features/alpha/internal", "../../shared/alpha"],
  ),
  policyGroup(
    "packages/native-bridge/src/index.ts",
    ["./internal", "@tauri-apps/api/core", "@tauri-apps/plugin-store"],
    ["@/ui"],
  ),
  policyGroup("src/i18n/index.ts", ["./internal"], ["@/ui"]),
  policyGroup("tooling/allowed-declaration.ts", [], ["@/features/alpha"]),
  policyGroup(
    "src/features/alpha/allowed-test-support.test.ts",
    ["@/test-support/harness"],
    ["@/test-support/harness/forbidden-api-internal"],
  ),
  policyGroup("src/app/allowed-native-config.test.ts", [
    "../../src-tauri/capabilities/default.json",
    "../../src-tauri/tauri.conf.json",
  ]),
  policyGroup(
    "src/features/beta/index.ts",
    [],
    ["@/features/alpha/styles.css"],
  ),
];

const dependencyFormCases = [
  [
    `import type { ApiFixtureType } from "@/api";`,
    `import type { BetaFixtureType } from "@/features/beta";`,
  ],
  [
    `export { apiValue } from "@/api";`,
    `export { betaValue } from "@/features/beta";`,
  ],
  [`void require("@/api");`, `void require("@/features/beta");`],
  [`void import("@/api");`, `void import("@/features/beta");`],
];
const vitestDependencyMethods = [
  "mock",
  "doMock",
  "unmock",
  "doUnmock",
  "importActual",
  "importMock",
];
const restrictedTauriModules = [
  "@tauri-apps/api",
  "@tauri-apps/api/core",
  "@tauri-apps/plugin-store",
];

test("desktop architecture policy enforces every owner and dependency form", async () => {
  const eslint = createArchitectureFixtureLinter();

  for (const group of architecturePolicyGroups) {
    for (const moduleSpecifier of group.allowedModules ?? []) {
      await assertArchitectureDiagnostics(
        eslint,
        group.filePath,
        staticImport(moduleSpecifier),
      );
    }
    for (const moduleSpecifier of group.forbiddenModules ?? []) {
      await assertArchitectureDiagnostics(
        eslint,
        group.filePath,
        staticImport(moduleSpecifier),
        dependencyDiagnostic,
      );
    }
  }

  for (const [allowedSource, forbiddenSource] of dependencyFormCases) {
    await assertArchitectureDiagnostics(
      eslint,
      "src/features/alpha/index.ts",
      allowedSource,
    );
    await assertArchitectureDiagnostics(
      eslint,
      "src/features/alpha/index.ts",
      forbiddenSource,
      dependencyDiagnostic,
    );
  }
  for (const method of vitestDependencyMethods) {
    const filePath = "src/features/alpha/allowed-test-support.test.ts";
    await assertArchitectureDiagnostics(
      eslint,
      filePath,
      vitestDependencySource(method, "@/app-facade"),
    );
    await assertArchitectureDiagnostics(
      eslint,
      filePath,
      vitestDependencySource(method, "@/features/beta"),
      [{ ruleId: "boundaries/dependencies", line: 2 }],
    );
  }
  for (const moduleSpecifier of restrictedTauriModules) {
    await assertArchitectureDiagnostics(
      eslint,
      "src/features/alpha/index.ts",
      staticImport(moduleSpecifier),
      [{ ruleId: "no-restricted-imports", line: 1 }],
    );
  }
  await assertArchitectureDiagnostics(
    eslint,
    "tooling/allowed-declaration.ts",
    `import type { ToolingDeclaration } from "@/types";`,
  );
  await assertArchitectureDiagnostics(
    eslint,
    "src/types/global.d.ts",
    `export {};
declare global {
  type ArchitectureFixtureDeclaration = string;
}`,
  );
  await assertArchitectureDiagnostics(
    eslint,
    "src/unknown-owner/file.ts",
    `export {};`,
    [{ ruleId: "boundaries/no-unknown-files", line: 1 }],
  );
  await assertArchitectureDiagnostics(
    eslint,
    "src/features/alpha/index.ts",
    staticImport("@/unknown-owner/file"),
    unknownDependencyDiagnostics,
  );
});

function findRule(name) {
  let result;
  for (const configEntry of eslintConfig) {
    if (
      configEntry.rules !== undefined &&
      Object.hasOwn(configEntry.rules, name)
    ) {
      result = configEntry.rules[name];
    }
  }
  return result;
}

function findRuleForFiles(name, files) {
  for (const configEntry of eslintConfig) {
    if (
      arrayEqual(configEntry.files, [files]) &&
      configEntry.rules !== undefined &&
      Object.hasOwn(configEntry.rules, name)
    ) {
      return configEntry.rules[name];
    }
  }
  return undefined;
}

function arrayEqual(left, right) {
  return (
    Array.isArray(left) &&
    left.length === right.length &&
    left.every((item, index) => item === right[index])
  );
}

function createArchitectureFixtureLinter() {
  return new ESLint({
    cwd: fixtureRoot,
    overrideConfigFile: true,
    overrideConfig: createArchitecturePolicy({
      rootPath: fixtureRoot,
      parserProjects: [join(fixtureRoot, "tsconfig.json")],
    }),
  });
}

async function assertArchitectureDiagnostics(
  eslint,
  filePath,
  source,
  expectedDiagnostics = [],
) {
  const [result] = await eslint.lintText(source, {
    filePath: join(fixtureRoot, filePath),
  });
  const actualDiagnostics = result.messages
    .map(({ ruleId, line }) => ({ ruleId, line }))
    .sort(compareArchitectureDiagnostics);
  const sortedExpected = [...expectedDiagnostics].sort(
    compareArchitectureDiagnostics,
  );

  assert.deepEqual(
    actualDiagnostics,
    sortedExpected,
    `unexpected architecture diagnostics for ${filePath}\n${source}`,
  );
}

function compareArchitectureDiagnostics(left, right) {
  return left.line - right.line || left.ruleId.localeCompare(right.ruleId);
}

function staticImport(moduleSpecifier) {
  return `import "${moduleSpecifier}";`;
}

function vitestDependencySource(method, moduleSpecifier) {
  return `import { vi } from "vitest";
void vi.${method}("${moduleSpecifier}");`;
}

function policyGroup(filePath, allowedModules, forbiddenModules = []) {
  return { filePath, allowedModules, forbiddenModules };
}

async function lintWithAppArchitectureRules(source) {
  const [result] = await desktopPolicyLinter.lintText(source, {
    filePath: join(desktopRoot, "src/app/debugFailure.ts"),
  });

  return result.messages;
}

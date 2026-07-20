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

test("desktop ESLint config explicitly forbids direct Tauri imports", () => {
  const rule = findRule("no-restricted-imports");

  assert.ok(Array.isArray(rule));
  assert.equal(rule[0], "error");

  const options = rule[1];
  assert.ok(options !== null && typeof options === "object");

  const paths = Array.isArray(options.paths) ? options.paths : [];
  assert.ok(paths.some((entry) => entry.name === "@tauri-apps/api"));

  const patterns = Array.isArray(options.patterns) ? options.patterns : [];
  assert.ok(
    patterns.some(
      (entry) =>
        Array.isArray(entry.group) && entry.group.includes("@tauri-apps/api/*"),
    ),
  );
});

test("desktop ESLint config explicitly enforces GUI architecture rules", () => {
  assert.equal(findRule("app/no-array-index-key"), "error");
  assert.equal(findRule("app/no-eslint-disable"), "error");
  assert.equal(findRule("app/no-mutable-exports"), "error");
  assert.equal(findRule("app/no-typeof-type-guards"), "error");
  assert.equal(findRule("app/no-useeffect-data-loading"), "error");
});

test("desktop ESLint config bans eslint-disable directives and makes the ban unsuppressable", () => {
  // The ban itself plus noInlineConfig: with inline directives neutralized, no rule —
  // including app/no-eslint-disable — can be turned off from within a source file.
  assert.equal(findRule("app/no-eslint-disable"), "error");
  assert.equal(findLinterOption("noInlineConfig"), true);
});

test("desktop ESLint rule rejects every eslint-disable/eslint-enable directive form", async () => {
  const directives = [
    "/* eslint-disable */",
    "/* eslint-disable max-lines -- fake reason */",
    "// eslint-disable-line no-console",
    "// eslint-disable-next-line complexity",
    "/* eslint-enable max-lines */",
  ];

  for (const directive of directives) {
    const messages = await lintWithAppArchitectureRules(
      `${directive}\nexport const value = 1;\n`,
    );
    assert.ok(
      messages.some((message) => message.ruleId === "app/no-eslint-disable"),
      `expected app/no-eslint-disable to flag ${directive}`,
    );
  }
});

test("desktop ESLint rule ignores ordinary comments that merely mention eslint", async () => {
  const messages = await lintWithAppArchitectureRules(
    "// eslint runs in CI to enforce these rules\nexport const value = 1;\n",
  );

  assert.ok(
    !messages.some((message) => message.ruleId === "app/no-eslint-disable"),
  );
});

test("noInlineConfig prevents suppressing the eslint-disable ban inline", async () => {
  const eslint = new ESLint({
    overrideConfigFile: true,
    overrideConfig: [
      { linterOptions: { noInlineConfig: true } },
      {
        files: ["**/*.tsx"],
        languageOptions: {
          ecmaVersion: "latest",
          sourceType: "module",
          parserOptions: { ecmaFeatures: { jsx: true } },
        },
        plugins: { app: findAppPlugin() },
        rules: { "app/no-eslint-disable": "error" },
      },
    ],
  });

  const source =
    "/* eslint-disable app/no-eslint-disable */\nexport const value = 1;\n";
  const [result] = await eslint.lintText(source, {
    filePath: "src/components/sample.tsx",
  });

  assert.ok(
    result.messages.some(
      (message) => message.ruleId === "app/no-eslint-disable",
    ),
  );
});

test("desktop ESLint architecture rules reject representative component violations", async () => {
  const messages = await lintWithAppArchitectureRules(`
    import { useEffect as useReactEffect } from "react";
    export let mutableSessionCount = 0;

    export function transcriptRows({ items }) {
      useReactEffect(() => {
        fetch("/api/sessions");
      }, []);

      return items.map((item, index) => <span key={index}>{item}</span>);
    }
  `);

  assert.deepEqual(messages.map((message) => message.ruleId).sort(), [
    "app/no-array-index-key",
    "app/no-mutable-exports",
    "app/no-useeffect-data-loading",
  ]);
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

test("desktop architecture policy allows owner-local imports and rejects feature dependencies", async () => {
  const byFile = await lintArchitectureFixtures([
    "src/features/alpha/allowed-same-feature.ts",
    "src/features/alpha/forbidden-feature.ts",
  ]);

  assert.deepEqual(
    byFile.get(join(fixtureRoot, "src/features/alpha/allowed-same-feature.ts")),
    [],
  );
  assert.ok(
    byFile
      .get(join(fixtureRoot, "src/features/alpha/forbidden-feature.ts"))
      ?.some((message) => message.ruleId === "boundaries/dependencies"),
  );
});

test("desktop architecture policy lets shell composition use public seams and rejects deep feature imports", async () => {
  const byFile = await lintArchitectureFixtures([
    "src/app/allowed-public-seams.ts",
    "src/app/forbidden-deep-facade.ts",
    "src/app/forbidden-deep-feature.ts",
    "src/app/forbidden-deep-native.ts",
    "src/app/forbidden-deep-shared.ts",
    "src/app/forbidden-deep-ui.ts",
  ]);

  assert.deepEqual(
    byFile.get(join(fixtureRoot, "src/app/allowed-public-seams.ts")),
    [],
  );
  assertArchitectureViolations(byFile, [
    "src/app/forbidden-deep-facade.ts",
    "src/app/forbidden-deep-feature.ts",
    "src/app/forbidden-deep-native.ts",
    "src/app/forbidden-deep-shared.ts",
    "src/app/forbidden-deep-ui.ts",
  ]);
});

test("desktop architecture policy keeps the app facade leaf-facing", async () => {
  const allowedPath = "src/app-facade/allowed-public-seams.ts";
  const forbiddenPaths = [
    "src/app-facade/forbidden-api-composition.ts",
    "src/app-facade/forbidden-feature.ts",
    "src/app-facade/forbidden-shell.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy isolates features behind feature-safe seams", async () => {
  const allowedPath = "src/features/alpha/allowed-public-seams.ts";
  const forbiddenPaths = [
    "src/features/alpha/forbidden-api-composition.ts",
    "src/features/alpha/forbidden-api-internal.ts",
    "src/features/alpha/forbidden-feature.ts",
    "src/features/alpha/forbidden-native.ts",
    "src/features/alpha/forbidden-relative-shared.ts",
    "src/features/alpha/forbidden-shell.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy permits only public shared-capability dependencies", async () => {
  const allowedPath = "src/shared/alpha/allowed-public-seams.ts";
  const forbiddenPaths = [
    "src/shared/alpha/forbidden-api-composition.ts",
    "src/shared/alpha/forbidden-deep-shared.ts",
    "src/shared/alpha/forbidden-feature.ts",
    "src/shared/alpha/forbidden-native.ts",
    "src/shared/alpha/forbidden-relative-shared.ts",
    "src/shared/alpha/forbidden-shell.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy keeps UI and API internals owner-local", async () => {
  const allowedPaths = [
    "src/api/allowed-owner-local.ts",
    "src/ui/allowed-owner-local.ts",
  ];
  const forbiddenPaths = [
    "src/api/forbidden-ui.ts",
    "src/ui/forbidden-api.ts",
    "src/ui/forbidden-feature.ts",
    "src/ui/forbidden-native.ts",
    "src/ui/forbidden-shell.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    ...allowedPaths,
    ...forbiddenPaths,
  ]);

  for (const path of allowedPaths) {
    assert.deepEqual(byFile.get(join(fixtureRoot, path)), []);
  }
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy gives test support only public production seams", async () => {
  const allowedPath = "src/test-support/harness/index.ts";
  const forbiddenPaths = [
    "src/test-support/harness/forbidden-api-internal.ts",
    "src/test-support/harness/forbidden-feature-internal.ts",
    "src/test-support/harness/forbidden-relative-shared.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy classifies native, i18n, vendor, tooling, and native-config leaves", async () => {
  const allowedPaths = [
    "packages/native-bridge/src/allowed-owner-local.ts",
    "src/app/allowed-public-seams.ts",
    "src/i18n/allowed-owner-local.ts",
    "tooling/allowed-declaration.ts",
  ];
  const forbiddenPaths = [
    "packages/native-bridge/src/forbidden-ui.ts",
    "src/app/forbidden-native-config.ts",
    "src/features/alpha/forbidden-vendor-deep.ts",
    "src/i18n/forbidden-ui.ts",
    "tooling/forbidden-feature.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    ...allowedPaths,
    ...forbiddenPaths,
  ]);

  for (const path of allowedPaths) {
    assert.deepEqual(byFile.get(join(fixtureRoot, path)), []);
  }
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy fails closed on unknown files and local dependencies", async () => {
  const unknownFilePath = "src/unknown-owner/file.ts";
  const unknownDependencyPath =
    "src/features/alpha/forbidden-unknown-dependency.ts";
  const byFile = await lintArchitectureFixtures([
    unknownFilePath,
    unknownDependencyPath,
  ]);

  assert.ok(
    byFile
      .get(join(fixtureRoot, unknownFilePath))
      ?.some((message) => message.ruleId === "boundaries/no-unknown-files"),
  );
  assert.ok(
    byFile
      .get(join(fixtureRoot, unknownDependencyPath))
      ?.some(
        (message) => message.ruleId === "boundaries/no-unknown-dependencies",
      ),
  );
});

test("desktop architecture policy covers every default dependency form", async () => {
  const allowedPaths = [
    "src/features/alpha/allowed-dynamic-import.ts",
    "src/features/alpha/allowed-reexport.ts",
    "src/features/alpha/allowed-require.ts",
    "src/features/alpha/allowed-type-import.ts",
  ];
  const forbiddenPaths = [
    "src/features/alpha/forbidden-dynamic-import.ts",
    "src/features/alpha/forbidden-reexport.ts",
    "src/features/alpha/forbidden-require.ts",
    "src/features/alpha/forbidden-type-import.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    ...allowedPaths,
    ...forbiddenPaths,
  ]);

  for (const path of allowedPaths) {
    assert.deepEqual(byFile.get(join(fixtureRoot, path)), []);
  }
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy treats every Vitest module literal as a dependency", async () => {
  const allowedPath = "src/features/alpha/allowed-vi-module-loaders.test.ts";
  const forbiddenPaths = [
    "src/features/alpha/forbidden-vi-do-mock.test.ts",
    "src/features/alpha/forbidden-vi-do-unmock.test.ts",
    "src/features/alpha/forbidden-vi-import-actual.test.ts",
    "src/features/alpha/forbidden-vi-import-mock.test.ts",
    "src/features/alpha/forbidden-vi-mock.test.ts",
    "src/features/alpha/forbidden-vi-unmock.test.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy restricts test support and native configuration to tests", async () => {
  const allowedPaths = [
    "src/app/allowed-native-config.test.ts",
    "src/features/alpha/allowed-test-support.test.ts",
  ];
  const forbiddenPaths = [
    "src/app/forbidden-native-config.ts",
    "src/features/alpha/forbidden-deep-test-support.test.ts",
    "src/features/alpha/forbidden-test-support.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    ...allowedPaths,
    ...forbiddenPaths,
  ]);

  for (const path of allowedPaths) {
    assert.deepEqual(byFile.get(join(fixtureRoot, path)), []);
  }
  assertArchitectureViolations(byFile, forbiddenPaths);
});

test("desktop architecture policy classifies CSS dependencies and declarations", async () => {
  const allowedPaths = [
    "src/features/alpha/allowed-css.ts",
    "src/types/global.d.ts",
  ];
  const forbiddenPath = "src/features/beta/forbidden-css.ts";
  const byFile = await lintArchitectureFixtures([
    ...allowedPaths,
    forbiddenPath,
  ]);

  for (const path of allowedPaths) {
    assert.deepEqual(byFile.get(join(fixtureRoot, path)), []);
  }
  assertArchitectureViolations(byFile, [forbiddenPath]);
});

test("desktop architecture policy confines external Tauri imports to the native package", async () => {
  const allowedPath = "packages/native-bridge/src/allowed-tauri.ts";
  const forbiddenPaths = [
    "src/features/alpha/forbidden-tauri-api.ts",
    "src/features/alpha/forbidden-tauri-plugin.ts",
  ];
  const byFile = await lintArchitectureFixtures([
    allowedPath,
    ...forbiddenPaths,
  ]);

  assert.deepEqual(byFile.get(join(fixtureRoot, allowedPath)), []);
  for (const path of forbiddenPaths) {
    assert.ok(
      byFile
        .get(join(fixtureRoot, path))
        ?.some((message) => message.ruleId === "no-restricted-imports"),
      `expected ${path} to violate no-restricted-imports`,
    );
  }
});

test("desktop architecture policy excludes lifecycle executable adapters from the native package", async () => {
  const forbiddenPath =
    "packages/native-bridge/src/forbidden-lifecycle-executable-adapter.ts";
  const byFile = await lintArchitectureFixtures([forbiddenPath]);

  assert.ok(
    byFile
      .get(join(fixtureRoot, forbiddenPath))
      ?.some((message) => message.ruleId === "no-restricted-imports"),
    `expected ${forbiddenPath} to violate no-restricted-imports`,
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

function findLinterOption(name) {
  let result;
  for (const configEntry of eslintConfig) {
    if (
      configEntry.linterOptions !== undefined &&
      Object.hasOwn(configEntry.linterOptions, name)
    ) {
      result = configEntry.linterOptions[name];
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

async function lintArchitectureFixtures(paths) {
  const eslint = new ESLint({
    cwd: fixtureRoot,
    overrideConfigFile: true,
    overrideConfig: createArchitecturePolicy({
      rootPath: fixtureRoot,
      parserProjects: [join(fixtureRoot, "tsconfig.json")],
    }),
  });
  const results = await eslint.lintFiles(paths);
  return new Map(results.map((result) => [result.filePath, result.messages]));
}

function assertArchitectureViolations(byFile, paths) {
  for (const path of paths) {
    assert.ok(
      byFile
        .get(join(fixtureRoot, path))
        ?.some((message) => message.ruleId === "boundaries/dependencies"),
      `expected ${path} to violate boundaries/dependencies`,
    );
  }
}

async function lintWithAppArchitectureRules(source) {
  const appPlugin = findAppPlugin();
  const eslint = new ESLint({
    overrideConfigFile: true,
    overrideConfig: [
      { linterOptions: { noInlineConfig: true } },
      {
        files: ["**/*.tsx"],
        languageOptions: {
          ecmaVersion: "latest",
          sourceType: "module",
          parserOptions: {
            ecmaFeatures: {
              jsx: true,
            },
          },
        },
        plugins: {
          app: appPlugin,
        },
        rules: {
          "app/no-array-index-key": "error",
          "app/no-eslint-disable": "error",
          "app/no-mutable-exports": "error",
          "app/no-typeof-type-guards": "error",
          "app/no-useeffect-data-loading": "error",
        },
      },
    ],
  });

  const [result] = await eslint.lintText(source, {
    filePath: "src/components/transcriptRows.tsx",
  });

  return result.messages;
}

function findAppPlugin() {
  for (const configEntry of eslintConfig) {
    if (configEntry.plugins?.app !== undefined) {
      return configEntry.plugins.app;
    }
  }

  throw new Error("desktop ESLint config is missing app plugin.");
}

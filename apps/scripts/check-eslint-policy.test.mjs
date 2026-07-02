import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

import eslintConfig from "../desktop/eslint.config.js";

const desktopRequire = createRequire(new URL("../desktop/package.json", import.meta.url));
const { ESLint } = desktopRequire("eslint");

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
  assert.deepEqual(findRule("@typescript-eslint/return-await"), ["error", "in-try-catch"]);
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
    patterns.some((entry) => Array.isArray(entry.group) && entry.group.includes("@tauri-apps/api/*")),
  );
});

test("desktop ESLint config explicitly enforces GUI architecture rules", () => {
  assert.equal(findRule("app/no-array-index-key"), "error");
  assert.equal(findRule("app/no-eslint-disable"), "error");
  assert.equal(findRule("app/no-mutable-exports"), "error");
  assert.equal(findRule("app/no-raw-dto-in-components"), "error");
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
    const messages = await lintWithAppArchitectureRules(`${directive}\nexport const value = 1;\n`);
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

  assert.ok(!messages.some((message) => message.ruleId === "app/no-eslint-disable"));
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

  const source = "/* eslint-disable app/no-eslint-disable */\nexport const value = 1;\n";
  const [result] = await eslint.lintText(source, { filePath: "src/components/sample.tsx" });

  assert.ok(result.messages.some((message) => message.ruleId === "app/no-eslint-disable"));
});

test("desktop ESLint architecture rules reject representative component violations", async () => {
  const messages = await lintWithAppArchitectureRules(`
    import { useEffect as useReactEffect } from "react";
    import { SessionDto } from "../protocol/session";
    export let mutableSessionCount = 0;

    export function transcriptRows({ items }) {
      useReactEffect(() => {
        fetch("/api/sessions");
      }, []);

      return items.map((item, index) => <span key={index}>{item}</span>);
    }
  `);

  assert.deepEqual(
    messages.map((message) => message.ruleId).sort(),
    [
      "app/no-array-index-key",
      "app/no-mutable-exports",
      "app/no-raw-dto-in-components",
      "app/no-useeffect-data-loading",
    ],
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

function findRule(name) {
  let result;
  for (const configEntry of eslintConfig) {
    if (configEntry.rules !== undefined && Object.hasOwn(configEntry.rules, name)) {
      result = configEntry.rules[name];
    }
  }
  return result;
}

function findLinterOption(name) {
  let result;
  for (const configEntry of eslintConfig) {
    if (configEntry.linterOptions !== undefined && Object.hasOwn(configEntry.linterOptions, name)) {
      result = configEntry.linterOptions[name];
    }
  }
  return result;
}

function findRuleForFiles(name, files) {
  for (const configEntry of eslintConfig) {
    if (arrayEqual(configEntry.files, [files]) && configEntry.rules !== undefined && Object.hasOwn(configEntry.rules, name)) {
      return configEntry.rules[name];
    }
  }
  return undefined;
}

function arrayEqual(left, right) {
  return Array.isArray(left) && left.length === right.length && left.every((item, index) => item === right[index]);
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
          "app/no-raw-dto-in-components": "error",
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

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { createArchitecturePolicy } from "../desktop/eslint-architecture.config.js";

const desktopRequire = createRequire(
  new URL("../desktop/package.json", import.meta.url),
);
const { ESLint } = desktopRequire("eslint");
const fixtureRoot = fileURLToPath(
  new URL("../desktop/eslint-fixtures/architecture", import.meta.url),
);

const allowedPaths = Object.freeze([
  "packages/native-bridge/src/allowed-owner-local.ts",
  "packages/native-bridge/src/allowed-tauri.ts",
  "src/api/allowed-owner-local.ts",
  "src/app-facade/allowed-public-seams.ts",
  "src/app/allowed-native-config.test.ts",
  "src/app/allowed-public-seams.ts",
  "src/features/alpha/allowed-css.ts",
  "src/features/alpha/allowed-dynamic-import.ts",
  "src/features/alpha/allowed-public-seams.ts",
  "src/features/alpha/allowed-reexport.ts",
  "src/features/alpha/allowed-require.ts",
  "src/features/alpha/allowed-same-feature.ts",
  "src/features/alpha/allowed-test-support.test.ts",
  "src/features/alpha/allowed-type-import.ts",
  "src/features/alpha/allowed-vi-module-loaders.test.ts",
  "src/i18n/allowed-owner-local.ts",
  "src/shared/alpha/allowed-public-seams.ts",
  "src/shared/labels/allowed-public-seams.ts",
  "src/test-support/harness/index.ts",
  "src/types/global.d.ts",
  "src/ui/allowed-owner-local.ts",
  "tooling/allowed-declaration.ts",
]);

const forbiddenDependencyPaths = Object.freeze([
  "packages/native-bridge/src/forbidden-ui.ts",
  "src/api/forbidden-ui.ts",
  "src/app-facade/forbidden-api-composition.ts",
  "src/app-facade/forbidden-feature.ts",
  "src/app-facade/forbidden-shell.ts",
  "src/app/forbidden-deep-facade.ts",
  "src/app/forbidden-deep-feature.ts",
  "src/app/forbidden-deep-native.ts",
  "src/app/forbidden-deep-shared.ts",
  "src/app/forbidden-deep-ui.ts",
  "src/app/forbidden-native-config.ts",
  "src/features/alpha/forbidden-api-composition.ts",
  "src/features/alpha/forbidden-api-internal.ts",
  "src/features/alpha/forbidden-deep-labels.ts",
  "src/features/alpha/forbidden-deep-test-support.test.ts",
  "src/features/alpha/forbidden-dynamic-import.ts",
  "src/features/alpha/forbidden-feature.ts",
  "src/features/alpha/forbidden-native.ts",
  "src/features/alpha/forbidden-reexport.ts",
  "src/features/alpha/forbidden-relative-shared.ts",
  "src/features/alpha/forbidden-require.ts",
  "src/features/alpha/forbidden-shell.ts",
  "src/features/alpha/forbidden-test-support.ts",
  "src/features/alpha/forbidden-type-import.ts",
  "src/features/alpha/forbidden-vendor-deep.ts",
  "src/features/alpha/forbidden-vi-do-mock.test.ts",
  "src/features/alpha/forbidden-vi-do-unmock.test.ts",
  "src/features/alpha/forbidden-vi-import-actual.test.ts",
  "src/features/alpha/forbidden-vi-import-mock.test.ts",
  "src/features/alpha/forbidden-vi-mock.test.ts",
  "src/features/alpha/forbidden-vi-unmock.test.ts",
  "src/features/beta/forbidden-css.ts",
  "src/i18n/forbidden-ui.ts",
  "src/shared/alpha/forbidden-api-composition.ts",
  "src/shared/alpha/forbidden-deep-shared.ts",
  "src/shared/alpha/forbidden-feature.ts",
  "src/shared/alpha/forbidden-native.ts",
  "src/shared/alpha/forbidden-relative-shared.ts",
  "src/shared/alpha/forbidden-shell.ts",
  "src/shared/labels/forbidden-api-internal.ts",
  "src/shared/labels/forbidden-feature.ts",
  "src/shared/labels/forbidden-native.ts",
  "src/shared/labels/forbidden-shell.ts",
  "src/test-support/harness/forbidden-api-internal.ts",
  "src/test-support/harness/forbidden-feature-internal.ts",
  "src/test-support/harness/forbidden-relative-shared.ts",
  "src/ui/forbidden-api.ts",
  "src/ui/forbidden-feature.ts",
  "src/ui/forbidden-native.ts",
  "src/ui/forbidden-shell.ts",
  "tooling/forbidden-feature.ts",
]);

const forbiddenTauriPaths = Object.freeze([
  "src/features/alpha/forbidden-tauri-api.ts",
  "src/features/alpha/forbidden-tauri-plugin.ts",
]);

const unknownFilePath = "src/unknown-owner/file.ts";
const unknownDependencyPath =
  "src/features/alpha/forbidden-unknown-dependency.ts";

test("desktop architecture policy exercises every locked fixture", async () => {
  const paths = [
    ...allowedPaths,
    ...forbiddenDependencyPaths,
    ...forbiddenTauriPaths,
    unknownFilePath,
    unknownDependencyPath,
  ];
  const messagesByPath = await lintArchitectureFixtures(paths);

  for (const path of allowedPaths) {
    assert.deepEqual(
      messagesFor(messagesByPath, path),
      [],
      `expected ${path} to be allowed`,
    );
  }
  for (const path of forbiddenDependencyPaths) {
    assertRule(messagesByPath, path, "boundaries/dependencies");
  }
  for (const path of forbiddenTauriPaths) {
    assertRule(messagesByPath, path, "no-restricted-imports");
  }
  assertRule(messagesByPath, unknownFilePath, "boundaries/no-unknown-files");
  assertRule(
    messagesByPath,
    unknownDependencyPath,
    "boundaries/no-unknown-dependencies",
  );
});

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

function messagesFor(messagesByPath, path) {
  const messages = messagesByPath.get(join(fixtureRoot, path));
  assert.ok(
    messages !== undefined,
    `ESLint did not return a result for ${path}`,
  );
  return messages;
}

function assertRule(messagesByPath, path, ruleId) {
  const messages = messagesFor(messagesByPath, path);
  assert.ok(
    messages.some((message) => message.ruleId === ruleId),
    `expected ${path} to violate ${ruleId}`,
  );
}

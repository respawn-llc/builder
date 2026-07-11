import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

import { appArchitecture } from "../desktop/eslint-app-plugin.js";

const desktopRequire = createRequire(
  new URL("../desktop/package.json", import.meta.url),
);
const { RuleTester } = desktopRequire("eslint");

RuleTester.setDefaultConfig({
  languageOptions: {
    ecmaVersion: "latest",
    sourceType: "module",
    parserOptions: {
      ecmaFeatures: {
        jsx: true,
      },
    },
  },
});

test("app/no-array-index-key rejects index-like keys only", () => {
  const tester = new RuleTester();

  tester.run(
    "app/no-array-index-key",
    appArchitecture.rules["no-array-index-key"],
    {
      valid: [
        {
          code: "items.map((item) => <span key={item.id}>{item.label}</span>);",
          filename: "src/components/transcriptRows.tsx",
        },
      ],
      invalid: [
        {
          code: "items.map((item, index) => <span key={index}>{item.label}</span>);",
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "indexKey" }],
        },
        {
          code: "items.map((item, idx) => <span key={idx}>{item.label}</span>);",
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "indexKey" }],
        },
      ],
    },
  );
});

test("app/no-raw-dto-in-components handles lowercase component files", () => {
  const tester = new RuleTester();

  tester.run(
    "app/no-raw-dto-in-components",
    appArchitecture.rules["no-raw-dto-in-components"],
    {
      valid: [
        {
          code: 'import { SessionViewModel } from "../view-models/session";',
          filename: "src/components/transcriptRows.tsx",
        },
        {
          code: 'import { SessionDto } from "../protocol/session";',
          filename: "src/hooks/useSessions.ts",
        },
      ],
      invalid: [
        {
          code: 'import { SessionDto } from "../view-models/session";',
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "rawDto" }],
        },
        {
          code: 'import { Session } from "../protocol/session";',
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "rawDto" }],
        },
      ],
    },
  );
});

test("app/no-useeffect-data-loading catches React.useEffect and aliased useEffect in components", () => {
  const tester = new RuleTester();

  tester.run(
    "app/no-useeffect-data-loading",
    appArchitecture.rules["no-useeffect-data-loading"],
    {
      valid: [
        {
          code: `
          import { useEffect } from "react";
          export function useSessions() {
            useEffect(() => {
              fetch("/api/sessions");
            }, []);
          }
        `,
          filename: "src/hooks/useSessions.tsx",
        },
        {
          code: `
          import { useEffect } from "react";
          export function transcriptRows() {
            useEffect(() => {
              window.setTimeout(() => undefined, 0);
            }, []);
          }
        `,
          filename: "src/components/transcriptRows.tsx",
        },
      ],
      invalid: [
        {
          code: `
          import * as React from "react";
          export function transcriptRows() {
            React.useEffect(() => {
              fetch("/api/sessions");
            }, []);
          }
        `,
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "dataLoading" }],
        },
        {
          code: `
          import { useEffect as useReactEffect } from "react";
          export function transcriptRows() {
            useReactEffect(() => {
              fetch("/api/sessions");
            }, []);
          }
        `,
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "dataLoading" }],
        },
        {
          code: `
          import { nativeBridge as desktopBridge } from "@app/native-bridge";
          import { useEffect } from "react";
          export function transcriptRows() {
            useEffect(() => {
              desktopBridge.clipboard.readText();
            }, []);
          }
        `,
          filename: "src/components/transcriptRows.tsx",
          errors: [{ messageId: "dataLoading" }],
        },
      ],
    },
  );
});

test("app/no-typeof-type-guards rejects runtime typeof checks except undefined globals", () => {
  const tester = new RuleTester();

  tester.run(
    "app/no-typeof-type-guards",
    appArchitecture.rules["no-typeof-type-guards"],
    {
      valid: [
        "const runtimeType = typeof value;",
        'function start() { if (typeof window === "undefined") return; }',
        'if ("undefined" !== typeof document) start();',
        'function read(value) { if (value === null) return; }',
      ],
      invalid: [
        {
          code: 'function read(value) { if (typeof value === "string") return value; }',
          errors: [{ messageId: "typeGuard" }],
        },
        {
          code: 'function read(value) { if ("number" !== typeof value) return; }',
          errors: [{ messageId: "typeGuard" }],
        },
        {
          code: 'const ready = typeof callback === "function";',
          errors: [{ messageId: "typeGuard" }],
        },
      ],
    },
  );
});

test("app/no-mutable-exports rejects exported let and var", () => {
  const tester = new RuleTester();

  tester.run(
    "app/no-mutable-exports",
    appArchitecture.rules["no-mutable-exports"],
    {
      valid: ["export const state = {};"],
      invalid: [
        {
          code: "export let state = {};",
          errors: [{ messageId: "mutableExport" }],
        },
        {
          code: "export var state = {};",
          errors: [{ messageId: "mutableExport" }],
        },
        {
          code: "let state = {}; export { state };",
          errors: [{ messageId: "mutableExport" }],
        },
        {
          code: "let state = {}; export { state as mutableState };",
          errors: [{ messageId: "mutableExport" }],
        },
      ],
    },
  );
});

assert.ok(appArchitecture.rules["no-array-index-key"]);

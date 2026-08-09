/// <reference types="node" />

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import * as ts from "typescript";
import { expect, it } from "vitest";

const forbiddenIdentifiers = new Set([
  "PromptFollowUpWatchRequest",
  "PromptFollowUpEvent",
  "subscribeFollowUp",
]);
const forbiddenWireValues = new Set([
  "prompt.followUp.watch",
  "prompt.followUp.event",
  "prompt.followUp.complete",
]);

it("keeps Desktop off the command-only prompt follow-up stream", () => {
  const root = join(process.cwd(), "src");
  const findings: string[] = [];
  for (const path of productionTypeScriptFiles(root)) {
    const text = readFileSync(path, "utf8");
    const scanner = ts.createScanner(
      ts.ScriptTarget.Latest,
      true,
      ts.LanguageVariant.JSX,
      text,
    );
    for (let token = scanner.scan(); token !== ts.SyntaxKind.EndOfFileToken; token = scanner.scan()) {
      const value = scanner.getTokenValue();
      if (token === ts.SyntaxKind.Identifier && forbiddenIdentifiers.has(value)) {
        findings.push(`${path}:${value}`);
      }
      if (
        (token === ts.SyntaxKind.StringLiteral ||
          token === ts.SyntaxKind.NoSubstitutionTemplateLiteral) &&
        forbiddenWireValues.has(value)
      ) {
        findings.push(`${path}:${value}`);
      }
    }
  }
  expect(findings).toEqual([]);
});

function productionTypeScriptFiles(root: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) {
      files.push(...productionTypeScriptFiles(path));
      continue;
    }
    if (
      (entry.name.endsWith(".ts") || entry.name.endsWith(".tsx")) &&
      !entry.name.endsWith(".test.ts") &&
      !entry.name.endsWith(".test.tsx")
    ) {
      files.push(path);
    }
  }
  return files;
}

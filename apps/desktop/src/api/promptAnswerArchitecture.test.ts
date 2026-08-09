/// <reference types="node" />

import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
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
    const source = ts.createSourceFile(
      path,
      readFileSync(path, "utf8"),
      ts.ScriptTarget.Latest,
      true,
      extname(path) === ".tsx" ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
    );
    const visit = (node: ts.Node): void => {
      if (ts.isIdentifier(node) && forbiddenIdentifiers.has(node.text)) {
        findings.push(
          `${path}:${(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1).toString()}:${node.text}`,
        );
      }
      if (
        (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
        forbiddenWireValues.has(node.text)
      ) {
        findings.push(
          `${path}:${(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1).toString()}:${node.text}`,
        );
      }
      ts.forEachChild(node, visit);
    };
    visit(source);
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

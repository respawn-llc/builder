/// <reference types="node" />

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";

const legacyTypeNames = new Set(["promptTemplate", "inputFields", "outputFields"]);
const legacyWireNames = new Set(["prompt_template", "input_fields", "output_fields"]);
const promptFollowUpSymbols = new Set([
  "PromptFollowUpWatchRequest",
  "PromptFollowUpEvent",
  "subscribeFollowUp",
  "prompt.followUp.watch",
  "prompt.followUp.event",
  "prompt.followUp.complete",
]);

function source(path: string): ts.SourceFile {
  return ts.createSourceFile(
    path,
    readFileSync(path, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
}

function typeAlias(sourceFile: ts.SourceFile, name: string): ts.TypeAliasDeclaration {
  const declaration = sourceFile.statements.find(
    (statement): statement is ts.TypeAliasDeclaration =>
      ts.isTypeAliasDeclaration(statement) && statement.name.text === name,
  );
  if (declaration === undefined) {
    throw new Error(`type alias ${name} is missing`);
  }
  return declaration;
}

function typeLiteralMembers(type: ts.TypeNode): readonly ts.TypeElement[] {
  if (ts.isTypeLiteralNode(type)) {
    return type.members;
  }
  if (
    ts.isTypeReferenceNode(type) &&
    ts.isIdentifier(type.typeName) &&
    type.typeName.text === "Readonly" &&
    type.typeArguments?.length === 1
  ) {
    const argument = type.typeArguments[0];
    if (argument !== undefined) {
      return typeLiteralMembers(argument);
    }
  }
  throw new Error("expected a readonly object type literal");
}

function propertyName(member: ts.TypeElement): string | undefined {
  if (!ts.isPropertySignature(member)) {
    return undefined;
  }
  return ts.isIdentifier(member.name) || ts.isStringLiteral(member.name) ? member.name.text : undefined;
}

function typeNames(type: ts.TypeNode): string[] {
  if (ts.isTypeLiteralNode(type)) {
    return type.members.map(propertyName).filter((name): name is string => name !== undefined);
  }
  if (ts.isIntersectionTypeNode(type)) {
    return type.types.flatMap(typeNames);
  }
  if (ts.isTypeReferenceNode(type) && ts.isIdentifier(type.typeName) && type.typeArguments !== undefined) {
    if (type.typeName.text === "Omit") {
      const omitted = type.typeArguments[1];
      if (omitted === undefined) {
        return [];
      }
      return omitted.getChildren().flatMap((child) => {
        if (ts.isStringLiteral(child)) {
          return [child.text];
        }
        return [];
      });
    }
    if (type.typeName.text === "Readonly") {
      const argument = type.typeArguments[0];
      return argument === undefined ? [] : typeNames(argument);
    }
  }
  return [];
}

function canonicalPath(relativePath: string): string {
  return fileURLToPath(new URL(relativePath, import.meta.url));
}

function nodePayloadFromMapCall(
  node: ts.Node,
  sourceFile: ts.SourceFile,
): ts.ObjectLiteralExpression | undefined {
  if (!ts.isCallExpression(node) || !ts.isPropertyAccessExpression(node.expression)) {
    return undefined;
  }
  if (node.expression.name.text !== "map") {
    return undefined;
  }
  const callback = node.arguments[0];
  if (
    callback === undefined ||
    !ts.isArrowFunction(callback) ||
    callback.parameters[0]?.name.getText(sourceFile) !== "node"
  ) {
    return undefined;
  }
  if (ts.isObjectLiteralExpression(callback.body)) {
    return callback.body;
  }
  if (!ts.isCallExpression(callback.body)) {
    return undefined;
  }
  const payloadArgument = callback.body.arguments[0];
  return payloadArgument !== undefined && ts.isObjectLiteralExpression(payloadArgument)
    ? payloadArgument
    : undefined;
}

function findNodePayload(sourceFile: ts.SourceFile): ts.ObjectLiteralExpression | undefined {
  let payload: ts.ObjectLiteralExpression | undefined;
  function visit(node: ts.Node): void {
    payload ??= nodePayloadFromMapCall(node, sourceFile);
    if (payload === undefined) {
      ts.forEachChild(node, visit);
    }
  }
  ts.forEachChild(sourceFile, visit);
  return payload;
}

describe("workflow Node contract ownership", () => {
  it("keeps Desktop off the command-only prompt follow-up stream", () => {
    const findings: string[] = [];
    for (const path of ts.sys.readDirectory(
      canonicalPath("../"),
      [".ts", ".tsx"],
      ["**/*.test.ts", "**/*.test.tsx"],
    )) {
      const sourceFile = source(path);
      const visit = (node: ts.Node): void => {
        if (
          (ts.isIdentifier(node) || ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
          promptFollowUpSymbols.has(node.text)
        ) {
          findings.push(`${path}:${node.text}`);
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    }
    expect(findings).toEqual([]);
  });

  it("removes legacy fields from canonical Node and draft types", () => {
    const models = source(canonicalPath("./models.ts"));
    for (const name of ["WorkflowNode", "WorkflowGraphDraftNode"]) {
      const members = typeLiteralMembers(typeAlias(models, name).type);
      expect(members.map(propertyName).filter((name): name is string => name !== undefined)).not.toEqual(
        expect.arrayContaining([...legacyTypeNames]),
      );
    }

    const draftTypes = source(canonicalPath("../features/workflow-editor/workflowEditorDraftTypes.ts"));
    const draft = typeAlias(draftTypes, "DraftWorkflowNode");
    expect(typeNames(draft.type)).not.toEqual(expect.arrayContaining([...legacyTypeNames]));
  });

  it("does not serialize legacy fields in the graph Node payload", () => {
    const graphClient = source(canonicalPath("./clientWorkflowGraph.ts"));
    const nodePayload = findNodePayload(graphClient);
    if (nodePayload === undefined) {
      throw new Error("workflow Node payload map is missing");
    }
    const properties = nodePayload.properties
      .filter((property): property is ts.PropertyAssignment => ts.isPropertyAssignment(property))
      .map((property) => property.name.getText(graphClient));
    expect(properties).not.toEqual(expect.arrayContaining([...legacyWireNames]));
  });
});

/// <reference types="node" />

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";

const legacyTypeNames = new Set(["promptTemplate", "inputFields", "outputFields"]);
const legacyWireNames = new Set(["prompt_template", "input_fields", "output_fields"]);

function source(path: string): ts.SourceFile {
  return ts.createSourceFile(
    path,
    readFileSync(path, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
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
  if (!ts.isPropertySignature(member) || member.name === undefined) {
    return undefined;
  }
  return ts.isIdentifier(member.name) || ts.isStringLiteral(member.name)
    ? member.name.text
    : undefined;
}

function typeNames(type: ts.TypeNode): string[] {
  if (ts.isTypeLiteralNode(type)) {
    return type.members
      .map(propertyName)
      .filter((name): name is string => name !== undefined);
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

describe("workflow Node contract ownership", () => {
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
    let nodePayload: ts.ObjectLiteralExpression | undefined;
    ts.forEachChild(graphClient, function visit(node) {
      const callback = ts.isCallExpression(node) ? node.arguments[0] : undefined;
      if (
        ts.isCallExpression(node) &&
        ts.isPropertyAccessExpression(node.expression) &&
        node.expression.name.text === "map" &&
        callback !== undefined &&
        ts.isArrowFunction(callback) &&
        callback.parameters[0]?.name.getText(graphClient) === "node"
      ) {
        if (ts.isObjectLiteralExpression(callback.body)) {
          nodePayload = callback.body;
        } else if (ts.isCallExpression(callback.body)) {
          const payloadArgument = callback.body.arguments[0];
          if (payloadArgument !== undefined && ts.isObjectLiteralExpression(payloadArgument)) {
            nodePayload = payloadArgument;
          }
        }
      }
      ts.forEachChild(node, visit);
    });
    if (nodePayload === undefined) {
      throw new Error("workflow Node payload map is missing");
    }
    const properties = nodePayload.properties
      .filter((property): property is ts.PropertyAssignment => ts.isPropertyAssignment(property))
      .map((property) => property.name.getText(graphClient));
    expect(properties).not.toEqual(expect.arrayContaining([...legacyWireNames]));
  });
});

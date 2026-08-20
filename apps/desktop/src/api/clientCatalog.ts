import type { z } from "zod";

import { CatalogContractError, ContractError } from "./errors";
import { create, operationFromDescriptor } from "@app/server-api-contract";
import {
  SessionCatalogService,
  SessionCategory as GeneratedSessionCategory,
  type SessionSummary,
} from "@app/server-api-contract/gen/kent/api/project/session_catalog_pb";
import {
  sessionCatalogPageSize,
  type SessionCatalogPage,
  type SessionCatalogSummary,
  type SessionCategory,
} from "./models";
import { canonicalProjectIDSchema, sessionCategorySchema, sessionPageOffsetSchema } from "./schemas/catalog";
import type { DescriptorRpcTransport } from "./transport";
import { timestampMillis } from "./clientTime";

export function parseCatalogInput<T>(operation: string, schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value);
  if (result.success) return result.data;
  throw new ContractError(
    `${operation} did not match the catalog contract.`,
    result.error.issues.map((issue) => ({
      code: issue.code,
      path: issue.path.map(String),
    })),
  );
}

export function requireCatalogProject(method: string, expected: string, actual: string): void {
  if (actual !== expected) throw CatalogContractError.projectMismatch(method, expected, actual);
}

export async function listSessionPage(
  transport: DescriptorRpcTransport,
  projectID: string,
  category: SessionCategory,
  offset: number,
): Promise<SessionCatalogPage> {
  const method = SessionCatalogService.method.page;
  const operation = operationFromDescriptor(method).name;
  const expectedProjectID = parseCatalogInput(`${operation} project ID`, canonicalProjectIDSchema, projectID);
  const expectedCategory = parseCatalogInput(`${operation} category`, sessionCategorySchema, category);
  const expectedOffset = parseCatalogInput(`${operation} offset`, sessionPageOffsetSchema, offset);
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      projectId: expectedProjectID,
      category: sessionCategoryToGenerated(expectedCategory),
      offset: expectedOffset,
      limit: sessionCatalogPageSize,
    }),
  );
  if (result.outcome.case !== "success") {
    throw new ContractError(
      `${operation} failed with code ${result.outcome.case === "error" ? result.outcome.value.code : "missing_outcome"}.`,
    );
  }
  const success = result.outcome.value;
  if (success.sessions.length > sessionCatalogPageSize) {
    throw CatalogContractError.malformedResponse(
      operation,
      new ContractError(`Session page exceeds the requested ${String(sessionCatalogPageSize)} rows.`),
    );
  }
  const response: SessionCatalogPage = {
    projectID: success.projectId,
    category: sessionCategoryFromGenerated(success.category),
    sessions: success.sessions.map(sessionSummary),
    nextOffset: success.nextOffset ?? null,
  };
  requireCatalogProject(operation, expectedProjectID, response.projectID);
  if (response.category !== expectedCategory) {
    throw CatalogContractError.sessionCategoryMismatch(operation, expectedCategory, response.category);
  }
  return response;
}

function sessionSummary(summary: SessionSummary): SessionCatalogSummary {
  if (summary.updatedAt === undefined) {
    throw new ContractError("Session catalog summary timestamp is required.");
  }
  return {
    id: summary.sessionId,
    category: sessionCategoryFromGenerated(summary.category),
    name: summary.name ?? null,
    firstPromptPreview: summary.firstPromptPreview ?? null,
    updatedAt: timestampMillis(summary.updatedAt),
  };
}

function sessionCategoryToGenerated(category: SessionCategory): GeneratedSessionCategory {
  switch (category) {
    case "main":
      return GeneratedSessionCategory.MAIN;
    case "subagent":
      return GeneratedSessionCategory.SUBAGENT;
  }
}

function sessionCategoryFromGenerated(category: GeneratedSessionCategory): SessionCategory {
  switch (category) {
    case GeneratedSessionCategory.MAIN:
      return "main";
    case GeneratedSessionCategory.SUBAGENT:
      return "subagent";
    case GeneratedSessionCategory.UNSPECIFIED:
      throw new ContractError("Session category is unspecified.");
  }
}

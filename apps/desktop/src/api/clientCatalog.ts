import type { z } from "zod";

import { CatalogContractError, ContractError } from "./errors";
import { parseRpcResponse } from "./clientParse";
import type { JsonObject } from "./json";
import { sessionCatalogPageSize, type SessionCategory } from "./models";
import { canonicalProjectIDSchema, sessionCategorySchema, sessionPageOffsetSchema } from "./schemas/catalog";

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

export function parseCatalogResponse<T>(method: string, schema: z.ZodType<T>, value: unknown): T {
  try {
    return parseRpcResponse(method, schema, value);
  } catch (error) {
    throw error instanceof ContractError ? CatalogContractError.malformedResponse(method, error) : error;
  }
}

export function requireCatalogProject(method: string, expected: string, actual: string): void {
  if (actual !== expected) throw CatalogContractError.projectMismatch(method, expected, actual);
}

export function sessionPageCall(projectID: string, category: SessionCategory, offset: number) {
  const expectedProjectID = parseCatalogInput("session.page project ID", canonicalProjectIDSchema, projectID);
  const expectedCategory = parseCatalogInput("session.page category", sessionCategorySchema, category);
  const expectedOffset = parseCatalogInput("session.page offset", sessionPageOffsetSchema, offset);
  const params: JsonObject = {
    project_id: expectedProjectID,
    category: expectedCategory,
    offset: expectedOffset,
    limit: sessionCatalogPageSize,
  };
  return { expectedProjectID, expectedCategory, params };
}

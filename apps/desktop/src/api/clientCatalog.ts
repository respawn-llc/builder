import type { z } from "zod";

import { CatalogContractError, ContractError } from "./errors";
import { parseRpcResponse } from "./clientParse";

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

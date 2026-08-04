import { type z } from "zod";

import { ContractError } from "./errors";

export function parseRpcResponse<T>(method: string, schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value);
  if (!result.success) {
    throw new ContractError(
      `${method} response did not match GUI contract.`,
      result.error.issues.map((issue) => ({
        code: issue.code,
        path: issue.path.map(String),
      })),
    );
  }
  return result.data;
}

export function requireTaskBoundItems(taskID: string, items: readonly Readonly<{ taskID: string }>[]): void {
  if (items.some((item) => item.taskID !== taskID)) {
    throw new ContractError("response contains an item for another task.");
  }
}

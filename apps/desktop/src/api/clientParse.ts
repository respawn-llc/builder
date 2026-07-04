import { type z } from "zod";

import { ContractError } from "./errors";

export function parseRpcResponse<T>(method: string, schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value);
  if (!result.success) {
    throw new ContractError(`${method} response did not match GUI contract.`);
  }
  return result.data;
}

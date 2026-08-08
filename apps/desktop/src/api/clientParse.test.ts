import { z } from "zod";
import { describe, expect, it } from "vitest";

import { ContractError, errorMessage } from "./errors";
import { parseRpcResponse } from "./clientParse";

describe("parseRpcResponse", () => {
  it("retains safe schema diagnostics without rejected response values", () => {
    const schema = z.object({
      task: z.object({
        current_nodes: z.array(z.object({ session_id: z.string() })),
      }),
    });

    const error = catchError(() =>
      parseRpcResponse("workflow.task.get", schema, {
        task: {
          current_nodes: [{ session_id: { secret: "do-not-log" } }],
        },
      }),
    );

    expect(error).toBeInstanceOf(ContractError);
    expect(error).toMatchObject({
      diagnostics: [
        {
          code: "invalid_type",
          path: ["task", "current_nodes", "0", "session_id"],
        },
      ],
    });
    expect(errorMessage(error)).not.toContain("do-not-log");
  });
});

function catchError(operation: () => void): unknown {
  try {
    operation();
  } catch (error) {
    return error;
  }
  throw new Error("operation did not fail");
}

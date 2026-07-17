import { z } from "zod";

import { ContractError } from "./errors";
import type { RpcEventHandler } from "./transport";

export type WorkflowProjectEvent = Readonly<{
  action: string;
  changedIDs: readonly string[];
  occurredAtUnixMs: number;
  projectID: string | null;
  resource: string;
  workflowID: string | null;
}>;

export type WorkflowProjectEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorkflowProjectEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

const workflowProjectEventParamsSchema = z
  .object({
    event: z.object({
      action: z.string().min(1),
      changed_ids: z.array(z.string().min(1)).optional().default([]),
      occurred_at_unix_ms: z.number().int().positive(),
      project_id: z.string().min(1).optional(),
      resource: z.string().min(1),
      workflow_id: z.string().min(1).optional(),
    }),
  })
  .transform(({ event }): { event: WorkflowProjectEvent } => ({
    event: {
      action: event.action,
      changedIDs: event.changed_ids,
      occurredAtUnixMs: event.occurred_at_unix_ms,
      projectID: event.project_id ?? null,
      resource: event.resource,
      workflowID: event.workflow_id ?? null,
    },
  }));

export function workflowProjectEventRpcHandler(
  handler: WorkflowProjectEventHandler,
): RpcEventHandler {
  return {
    ...(handler.onOpen === undefined ? {} : { onOpen: handler.onOpen }),
    onComplete: handler.onComplete,
    onError: handler.onError,
    onEvent(method, params) {
      if (method !== "workflow.event") {
        return;
      }
      const parsed = workflowProjectEventParamsSchema.safeParse(params);
      if (!parsed.success) {
        handler.onError(new ContractError("workflow event did not match GUI contract."));
        return;
      }
      handler.onEvent(parsed.data.event);
    },
  };
}

import { z } from "zod";

import { ContractError } from "./errors";
import { workflowIDSchema } from "./schemas/common";
import type { RpcEventHandler } from "./transport";

export const workflowProjectEventResources = ["workflow", "workflow_link", "task", "label"] as const;
export type WorkflowProjectEventResource = (typeof workflowProjectEventResources)[number];

export const workflowProjectEventActions = [
  "created",
  "updated",
  "renamed",
  "deleted",
  "reordered",
  "node_added",
  "node_updated",
  "node_group_added",
  "node_group_updated",
  "node_group_deleted",
  "transition_group_added",
  "transition_group_updated",
  "edge_added",
  "edge_updated",
  "graph_saved",
  "linked",
  "default_changed",
  "unlinked",
  "started",
  "interrupted",
  "resumed",
  "approved",
  "moved",
  "completed",
  "comment_added",
  "comment_updated",
  "comment_deleted",
  "question_waiting",
  "question_cleared",
  "question_answered",
  "labels_changed",
  "dependencies_changed",
] as const;
export type WorkflowProjectEventAction = (typeof workflowProjectEventActions)[number];

export type WorkflowProjectEvent = Readonly<{
  action: WorkflowProjectEventAction;
  occurredAtUnixMs: number;
  primaryEntityID: string;
  projectID: string | null;
  relatedIDs: readonly string[];
  resource: WorkflowProjectEventResource;
  workflowID: string | null;
}>;

export type WorkflowProjectEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorkflowProjectEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

export type WorkflowProjectEventMethod = "workflow.event" | "workflow.project";

const workflowProjectEventResourceSchema = z.enum(workflowProjectEventResources);
const workflowProjectEventActionSchema = z.enum(workflowProjectEventActions);

const allowedActions: Readonly<
  Record<WorkflowProjectEventResource, ReadonlySet<WorkflowProjectEventAction>>
> = {
  label: new Set(["created", "renamed", "deleted", "reordered"]),
  task: new Set([
    "created",
    "updated",
    "deleted",
    "started",
    "interrupted",
    "resumed",
    "approved",
    "moved",
    "completed",
    "comment_added",
    "comment_updated",
    "comment_deleted",
    "question_waiting",
    "question_cleared",
    "question_answered",
    "labels_changed",
    "dependencies_changed",
  ]),
  workflow: new Set([
    "updated",
    "deleted",
    "node_added",
    "node_updated",
    "node_group_added",
    "node_group_updated",
    "node_group_deleted",
    "transition_group_added",
    "transition_group_updated",
    "edge_added",
    "edge_updated",
    "graph_saved",
  ]),
  workflow_link: new Set(["linked", "default_changed", "unlinked"]),
};

const workflowProjectEventWireSchema = z
  .object({
    action: workflowProjectEventActionSchema,
    occurred_at_unix_ms: z.number().int().positive(),
    primary_entity_id: z.string().min(1),
    project_id: z.string().min(1).optional(),
    related_ids: z.array(z.string().min(1)).optional().default([]),
    resource: workflowProjectEventResourceSchema,
    workflow_id: workflowIDSchema.optional(),
  })
  .superRefine((event, ctx) => {
    if (!allowedActions[event.resource].has(event.action)) {
      ctx.addIssue({ code: "custom", message: "action is not valid for resource", path: ["action"] });
    }
    if (event.resource === "workflow") {
      if (event.workflow_id === undefined) {
        ctx.addIssue({ code: "custom", message: "workflow_id is required", path: ["workflow_id"] });
      }
    } else if (event.resource === "label") {
      if (event.project_id === undefined) {
        ctx.addIssue({ code: "custom", message: "project_id is required", path: ["project_id"] });
      }
      if (event.workflow_id !== undefined) {
        ctx.addIssue({
          code: "custom",
          message: "workflow_id must be absent for label events",
          path: ["workflow_id"],
        });
      }
    } else {
      if (event.project_id === undefined) {
        ctx.addIssue({ code: "custom", message: "project_id is required", path: ["project_id"] });
      }
      if (event.workflow_id === undefined) {
        ctx.addIssue({ code: "custom", message: "workflow_id is required", path: ["workflow_id"] });
      }
    }
    const ids = new Set([event.primary_entity_id]);
    for (const [index, relatedID] of event.related_ids.entries()) {
      if (ids.has(relatedID)) {
        ctx.addIssue({
          code: "custom",
          message: "related_ids must be unique and must not repeat primary_entity_id",
          path: ["related_ids", index],
        });
      }
      ids.add(relatedID);
    }
  });

const workflowProjectEventParamsSchema = z
  .object({
    event: workflowProjectEventWireSchema,
  })
  .transform(({ event }): { event: WorkflowProjectEvent } => ({
    event: {
      action: event.action,
      occurredAtUnixMs: event.occurred_at_unix_ms,
      primaryEntityID: event.primary_entity_id,
      projectID: event.project_id ?? null,
      relatedIDs: event.related_ids,
      resource: event.resource,
      workflowID: event.workflow_id ?? null,
    },
  }));

export function workflowProjectEventRpcHandler(
  expectedMethod: WorkflowProjectEventMethod,
  handler: WorkflowProjectEventHandler,
): RpcEventHandler {
  return {
    ...(handler.onOpen === undefined ? {} : { onOpen: handler.onOpen }),
    onComplete: handler.onComplete,
    onError: handler.onError,
    onEvent(method, params) {
      if (method !== expectedMethod) {
        handler.onError(
          new ContractError(
            `workflow subscription expected event method ${expectedMethod} but received ${method}.`,
          ),
        );
        return;
      }
      const parsed = workflowProjectEventParamsSchema.safeParse(params);
      if (!parsed.success) {
        handler.onError(new ContractError(`${expectedMethod} payload did not match GUI contract.`));
        return;
      }
      handler.onEvent(parsed.data.event);
    },
  };
}

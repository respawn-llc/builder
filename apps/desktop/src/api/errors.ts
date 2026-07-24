import { z } from "zod";

import type { JsonValue } from "./json";
import type { TaskStatusKind } from "./models";
import { taskStatusKinds } from "./models";
import { rpcErrorCodes } from "./rpcErrorCodes";
import { labelIDSchema } from "./schemas/workflowLabels";
import { workflowLabelMaxIDs } from "./workflowLabelContract";

export type RpcErrorInfo = Readonly<{
  code: number;
  message: string;
  method: string;
  data?: JsonValue | undefined;
}>;

export class RpcError extends Error {
  readonly code: number;
  readonly method: string;
  readonly data: JsonValue | undefined;

  constructor(info: RpcErrorInfo) {
    super(info.message);
    this.name = "RpcError";
    this.code = info.code;
    this.method = info.method;
    this.data = info.data;
  }
}

export const workflowLabelErrorReasons = [
  "invalid_name",
  "name_conflict",
  "catalog_limit",
  "project_not_found",
  "label_not_found",
  "task_not_found",
  "wrong_project",
  "invalid_filter",
  "invalid_mutation",
] as const;
export type WorkflowLabelErrorReason = (typeof workflowLabelErrorReasons)[number];

export class WorkflowLabelError extends RpcError {
  readonly reason: WorkflowLabelErrorReason;
  readonly projectID: string | null;
  readonly taskID: string | null;
  readonly labelID: string | null;
  readonly field: string | null;
  readonly limit: number | null;

  constructor(
    rpcError: RpcError,
    info: Readonly<{
      reason: WorkflowLabelErrorReason;
      projectID: string | null;
      taskID: string | null;
      labelID: string | null;
      field: string | null;
      limit: number | null;
    }>,
  ) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.name = "WorkflowLabelError";
    this.reason = info.reason;
    this.projectID = info.projectID;
    this.taskID = info.taskID;
    this.labelID = info.labelID;
    this.field = info.field;
    this.limit = info.limit;
  }
}

const requiredIDSchema = z.string().trim().min(1);
const workflowLabelErrorDataSchema = z
  .object({
    type: z.literal("workflow_label_error"),
    reason: z.enum(workflowLabelErrorReasons),
    project_id: requiredIDSchema.optional(),
    task_id: requiredIDSchema.optional(),
    label_id: labelIDSchema.optional(),
    field: requiredIDSchema.optional(),
    limit: z.number().int().positive().optional(),
  })
  .strict()
  .superRefine((data, context) => {
    const required = (field: "project_id" | "task_id" | "label_id" | "field") => {
      if (data[field] === undefined) {
        context.addIssue({ code: "custom", message: `${field} is required`, path: [field] });
      }
    };
    switch (data.reason) {
      case "invalid_name":
        required("project_id");
        if (data.field !== "name") {
          context.addIssue({ code: "custom", message: "field must be name", path: ["field"] });
        }
        break;
      case "name_conflict":
      case "project_not_found":
        required("project_id");
        break;
      case "catalog_limit":
        required("project_id");
        if (data.limit !== workflowLabelMaxIDs) {
          context.addIssue({
            code: "custom",
            message: `limit must be ${String(workflowLabelMaxIDs)}`,
            path: ["limit"],
          });
        }
        break;
      case "label_not_found":
        required("label_id");
        break;
      case "task_not_found":
        required("task_id");
        break;
      case "wrong_project":
        required("project_id");
        required("label_id");
        break;
      case "invalid_filter":
      case "invalid_mutation":
        required("field");
        break;
    }
  })
  .transform((data) => ({
    reason: data.reason,
    projectID: data.project_id ?? null,
    taskID: data.task_id ?? null,
    labelID: data.label_id ?? null,
    field: data.field ?? null,
    limit: data.limit ?? null,
  }));

export function decodeWorkflowLabelError(error: unknown): WorkflowLabelError | null {
  if (!(error instanceof RpcError)) {
    return null;
  }
  const parsed = workflowLabelErrorDataSchema.safeParse(error.data);
  if (!parsed.success) {
    return null;
  }
  return new WorkflowLabelError(error, parsed.data);
}

export const workflowTaskIntegrityReasons = [
  "current_run_missing",
  "agent_session_missing",
  "exact_execution_missing",
  "exact_execution_mismatch",
  "action_projection_invalid",
] as const;
export type WorkflowTaskIntegrityReason = (typeof workflowTaskIntegrityReasons)[number];
export type WorkflowTaskIntegrityNodeKind = "agent" | "script";
export type WorkflowTaskIntegrityDurableFacts = Readonly<{
  runPresent: boolean;
  started: boolean;
  completed: boolean;
  interrupted: boolean;
  waitingQuestion: boolean;
}>;
export type WorkflowTaskIntegrityExactFacts = Readonly<{
  present: boolean;
  kind: WorkflowTaskIntegrityNodeKind | null;
  sessionID: string | null;
  waitingQuestion: boolean;
}>;
export type WorkflowTaskIntegrityActionFacts = Readonly<{
  canInterrupt: boolean;
  canResume: boolean;
}>;

type WorkflowTaskIntegrityInfo = Readonly<{
  reason: WorkflowTaskIntegrityReason;
  taskID: string;
  placementID: string;
  nodeID: string;
  nodeKind: WorkflowTaskIntegrityNodeKind;
  runID: string | null;
  sessionID: string | null;
  generation: number | null;
  statusKind: TaskStatusKind;
  durable: WorkflowTaskIntegrityDurableFacts;
  exact: WorkflowTaskIntegrityExactFacts;
  actions: WorkflowTaskIntegrityActionFacts;
}>;

export class WorkflowTaskIntegrityError extends RpcError implements WorkflowTaskIntegrityInfo {
  readonly reason: WorkflowTaskIntegrityReason;
  readonly taskID: string;
  readonly placementID: string;
  readonly nodeID: string;
  readonly nodeKind: WorkflowTaskIntegrityNodeKind;
  readonly runID: string | null;
  readonly sessionID: string | null;
  readonly generation: number | null;
  readonly statusKind: TaskStatusKind;
  readonly durable: WorkflowTaskIntegrityDurableFacts;
  readonly exact: WorkflowTaskIntegrityExactFacts;
  readonly actions: WorkflowTaskIntegrityActionFacts;

  constructor(rpcError: RpcError, info: WorkflowTaskIntegrityInfo) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.name = "WorkflowTaskIntegrityError";
    this.reason = info.reason;
    this.taskID = info.taskID;
    this.placementID = info.placementID;
    this.nodeID = info.nodeID;
    this.nodeKind = info.nodeKind;
    this.runID = info.runID;
    this.sessionID = info.sessionID;
    this.generation = info.generation;
    this.statusKind = info.statusKind;
    this.durable = info.durable;
    this.exact = info.exact;
    this.actions = info.actions;
  }
}

const requiredUnpaddedStringSchema = z
  .string()
  .min(1)
  .refine((value) => value.trim() === value, "value must not have edge whitespace");
const workflowTaskIntegrityNodeKindSchema = z.enum(["agent", "script"]);
const workflowTaskIntegrityExactFactsSchema = z
  .object({
    present: z.boolean(),
    kind: workflowTaskIntegrityNodeKindSchema.optional(),
    session_id: requiredUnpaddedStringSchema.optional(),
    waiting_question: z.boolean(),
  })
  .strict()
  .superRefine((facts, context) => {
    if (facts.present && facts.kind === undefined) {
      context.addIssue({ code: "custom", message: "kind is required", path: ["kind"] });
    }
    if (!facts.present && (facts.kind !== undefined || facts.session_id !== undefined || facts.waiting_question)) {
      context.addIssue({ code: "custom", message: "absent exact execution has facts" });
    }
  });
const workflowTaskIntegrityDataSchema = z
  .object({
    type: z.literal("workflow_task_integrity_error"),
    reason: z.enum(workflowTaskIntegrityReasons),
    task_id: requiredUnpaddedStringSchema,
    placement_id: requiredUnpaddedStringSchema,
    node_id: requiredUnpaddedStringSchema,
    node_kind: workflowTaskIntegrityNodeKindSchema,
    run_id: requiredUnpaddedStringSchema.optional(),
    session_id: requiredUnpaddedStringSchema.optional(),
    generation: z.number().int().nonnegative().optional(),
    status_kind: z.enum(taskStatusKinds),
    durable: z
      .object({
        run_present: z.boolean(),
        started: z.boolean(),
        completed: z.boolean(),
        interrupted: z.boolean(),
        waiting_question: z.boolean(),
      })
      .strict(),
    exact: workflowTaskIntegrityExactFactsSchema,
    actions: z
      .object({
        can_interrupt: z.boolean(),
        can_resume: z.boolean(),
      })
      .strict(),
  })
  .strict()
  .superRefine((data, context) => {
    if (data.run_id !== undefined && data.generation === undefined) {
      context.addIssue({ code: "custom", message: "generation is required", path: ["generation"] });
    }
    if (data.run_id === undefined && data.generation !== undefined) {
      context.addIssue({ code: "custom", message: "generation requires run_id", path: ["generation"] });
    }
  })
  .transform(
    (data): WorkflowTaskIntegrityInfo => ({
      reason: data.reason,
      taskID: data.task_id,
      placementID: data.placement_id,
      nodeID: data.node_id,
      nodeKind: data.node_kind,
      runID: data.run_id ?? null,
      sessionID: data.session_id ?? null,
      generation: data.generation ?? null,
      statusKind: data.status_kind,
      durable: {
        runPresent: data.durable.run_present,
        started: data.durable.started,
        completed: data.durable.completed,
        interrupted: data.durable.interrupted,
        waitingQuestion: data.durable.waiting_question,
      },
      exact: {
        present: data.exact.present,
        kind: data.exact.kind ?? null,
        sessionID: data.exact.session_id ?? null,
        waitingQuestion: data.exact.waiting_question,
      },
      actions: {
        canInterrupt: data.actions.can_interrupt,
        canResume: data.actions.can_resume,
      },
    }),
  );

export function decodeWorkflowTaskIntegrityError(error: unknown): WorkflowTaskIntegrityError | null {
  if (!(error instanceof RpcError) || error.code !== rpcErrorCodes.workflowTaskIntegrity) {
    return null;
  }
  const parsed = workflowTaskIntegrityDataSchema.safeParse(error.data);
  if (!parsed.success) {
    return null;
  }
  return new WorkflowTaskIntegrityError(error, parsed.data);
}

export class TransportError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TransportError";
  }
}

export class ContractError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ContractError";
  }
}

export class ProtocolMismatchError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ProtocolMismatchError";
  }
}

export class StartupConfigurationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "StartupConfigurationError";
  }
}

export class ServerRootMismatchError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ServerRootMismatchError";
  }
}

export function errorMessage(error: unknown): string {
  const stringError = z.string().safeParse(error);
  if (stringError.success) {
    return normalizeMessage(stringError.data);
  }
  if (error instanceof Error) {
    return normalizeMessage(error.message);
  }
  const messageObject = z.object({ message: z.string() }).safeParse(error);
  if (messageObject.success) {
    return normalizeMessage(messageObject.data.message);
  }
  if (error !== null && Object(error) === error) {
    try {
      return normalizeMessage(JSON.stringify(error));
    } catch {
      return "Unknown error";
    }
  }
  return "Unknown error";
}

function normalizeMessage(message: string): string {
  const trimmed = message.trim();
  return trimmed.length > 0 ? trimmed : "Unknown error";
}

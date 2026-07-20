import { z } from "zod";

import type { JsonValue } from "./json";
import { labelIDSchema } from "./schemas/workflowLabels";

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
        if (data.limit !== 100) {
          context.addIssue({ code: "custom", message: "limit must be 100", path: ["limit"] });
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

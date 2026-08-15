import { z } from "zod";

import type { JsonValue } from "./json";
import { workflowIDSchema } from "./schemas/workflowID";
import { labelIDSchema } from "./schemas/workflowLabels";
import { workflowLabelMaxIDs } from "./workflowLabelContract";
import { rpcErrorCodes } from "./rpcErrorCodes";

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

export function isTaskMissingError(error: unknown): boolean {
  return error instanceof RpcError && error.code === rpcErrorCodes.workflowTaskNotFound;
}

const projectMissingDataSchema = z.looseObject({ reason: z.literal("project_not_found") });
export function isProjectMissingError(error: unknown): boolean {
  return (
    error instanceof RpcError &&
    (error.code === rpcErrorCodes.projectNotFound || projectMissingDataSchema.safeParse(error.data).success)
  );
}

export const workflowTaskCreateSelectionErrorReasons = [
  "no_linked_workflows",
  "workflow_not_linked",
  "ambiguous_without_default",
] as const;
export type WorkflowTaskCreateSelectionErrorReason = (typeof workflowTaskCreateSelectionErrorReasons)[number];

export class WorkflowTaskCreateSelectionError extends RpcError {
  readonly reason: WorkflowTaskCreateSelectionErrorReason;
  readonly projectID: string;
  readonly workflowID: string | null;

  constructor(
    rpcError: RpcError,
    info: Readonly<{
      reason: WorkflowTaskCreateSelectionErrorReason;
      projectID: string;
      workflowID: string | null;
    }>,
  ) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.name = "WorkflowTaskCreateSelectionError";
    this.reason = info.reason;
    this.projectID = info.projectID;
    this.workflowID = info.workflowID;
  }
}

const workflowTaskCreateSelectionErrorDataSchema = z
  .object({
    type: z.literal("workflow_task_create_selection_error"),
    reason: z.enum(workflowTaskCreateSelectionErrorReasons),
    project_id: z.string().trim().min(1),
    workflow_id: workflowIDSchema.optional(),
  })
  .strict()
  .superRefine((data, context) => {
    const workflowRequired = data.reason === "workflow_not_linked";
    if (workflowRequired !== (data.workflow_id !== undefined)) {
      context.addIssue({
        code: "custom",
        message: workflowRequired
          ? "workflow_id is required for workflow_not_linked"
          : "workflow_id is forbidden for Project-scoped selection errors",
        path: ["workflow_id"],
      });
    }
  })
  .transform((data) => ({
    reason: data.reason,
    projectID: data.project_id,
    workflowID: data.workflow_id ?? null,
  }));

export function decodeWorkflowTaskCreateSelectionError(
  error: unknown,
): WorkflowTaskCreateSelectionError | null {
  if (
    !(error instanceof RpcError) ||
    error.code !== rpcErrorCodes.workflowTaskCreateSelection ||
    error.method !== "workflow.task.create"
  ) {
    return null;
  }
  const parsed = workflowTaskCreateSelectionErrorDataSchema.safeParse(error.data);
  return parsed.success ? new WorkflowTaskCreateSelectionError(error, parsed.data) : null;
}

export type TaskSearchErrorReason = "normalized_too_short";

export class TaskSearchError extends RpcError {
  readonly reason: TaskSearchErrorReason;

  constructor(rpcError: RpcError, reason: TaskSearchErrorReason) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.name = "TaskSearchError";
    this.reason = reason;
  }
}

const taskSearchErrorDataSchema = z
  .object({
    type: z.literal("task_search_error"),
    reason: z.literal("normalized_too_short"),
  })
  .strict();

export function decodeTaskSearchError(error: unknown): TaskSearchError | null {
  if (
    !(error instanceof RpcError) ||
    error.code !== rpcErrorCodes.workflowTaskSearch ||
    error.method !== "workflow.task.search"
  ) {
    return null;
  }
  const parsed = taskSearchErrorDataSchema.safeParse(error.data);
  return parsed.success ? new TaskSearchError(error, parsed.data.reason) : null;
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

export const workflowTaskDependencyErrorReasons = [
  "missing_task",
  "self_dependency",
  "project_mismatch",
  "reciprocal_dependency",
  "blocker_limit",
  "blocked_limit",
] as const;
export type WorkflowTaskDependencyErrorReason = (typeof workflowTaskDependencyErrorReasons)[number];

export class WorkflowTaskDependencyError extends RpcError {
  readonly reason: WorkflowTaskDependencyErrorReason;
  readonly blockerTaskID: string;
  readonly blockedTaskID: string;
  readonly missingTaskID: string | null;
  readonly currentCount: number | null;
  readonly limit: number | null;

  constructor(
    rpcError: RpcError,
    info: Readonly<{
      reason: WorkflowTaskDependencyErrorReason;
      blockerTaskID: string;
      blockedTaskID: string;
      missingTaskID: string | null;
      currentCount: number | null;
      limit: number | null;
    }>,
  ) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.name = "WorkflowTaskDependencyError";
    this.reason = info.reason;
    this.blockerTaskID = info.blockerTaskID;
    this.blockedTaskID = info.blockedTaskID;
    this.missingTaskID = info.missingTaskID;
    this.currentCount = info.currentCount;
    this.limit = info.limit;
  }
}

const workflowTaskDependencyErrorDataSchema = z
  .object({
    type: z.literal("workflow_task_dependency_error"),
    reason: z.enum(workflowTaskDependencyErrorReasons),
    blocker_task_id: requiredIDSchema,
    blocked_task_id: requiredIDSchema,
    missing_task_id: requiredIDSchema.optional(),
    current_count: z.number().int().nonnegative().optional(),
    limit: z.number().int().positive().optional(),
  })
  .strict()
  .superRefine(validateWorkflowTaskDependencyErrorData)
  .transform((data) => ({
    reason: data.reason,
    blockerTaskID: data.blocker_task_id,
    blockedTaskID: data.blocked_task_id,
    missingTaskID: data.missing_task_id ?? null,
    currentCount: data.current_count ?? null,
    limit: data.limit ?? null,
  }));

function validateWorkflowTaskDependencyErrorData(
  data: Readonly<{
    reason: WorkflowTaskDependencyErrorReason;
    missing_task_id?: string | undefined;
    current_count?: number | undefined;
    limit?: number | undefined;
  }>,
  context: z.RefinementCtx,
): void {
  if (data.reason === "missing_task") {
    validateMissingTaskDependencyError(data, context);
    return;
  }
  if (data.reason === "blocker_limit" || data.reason === "blocked_limit") {
    validateLimitedTaskDependencyError(data, context);
    return;
  }
  validateMetadataFreeTaskDependencyError(data, context);
}

function validateMissingTaskDependencyError(
  data: Readonly<{
    missing_task_id?: string | undefined;
    current_count?: number | undefined;
    limit?: number | undefined;
  }>,
  context: z.RefinementCtx,
): void {
  if (data.missing_task_id === undefined || data.current_count !== undefined || data.limit !== undefined) {
    context.addIssue({ code: "custom", message: "invalid missing task metadata" });
  }
}

function validateLimitedTaskDependencyError(
  data: Readonly<{
    missing_task_id?: string | undefined;
    current_count?: number | undefined;
    limit?: number | undefined;
  }>,
  context: z.RefinementCtx,
): void {
  if (data.missing_task_id !== undefined) {
    context.addIssue({ code: "custom", message: "invalid limit metadata" });
    return;
  }
  if (data.current_count === undefined || data.limit === undefined) {
    context.addIssue({ code: "custom", message: "invalid limit metadata" });
    return;
  }
  if (data.current_count > data.limit) {
    context.addIssue({ code: "custom", message: "invalid limit metadata" });
  }
}

function validateMetadataFreeTaskDependencyError(
  data: Readonly<{
    missing_task_id?: string | undefined;
    current_count?: number | undefined;
    limit?: number | undefined;
  }>,
  context: z.RefinementCtx,
): void {
  if (data.missing_task_id !== undefined || data.current_count !== undefined || data.limit !== undefined) {
    context.addIssue({
      code: "custom",
      message: "unexpected dependency error metadata",
    });
  }
}

export function decodeWorkflowTaskDependencyError(error: unknown): WorkflowTaskDependencyError | null {
  if (!(error instanceof RpcError)) {
    return null;
  }
  const parsed = workflowTaskDependencyErrorDataSchema.safeParse(error.data);
  return parsed.success ? new WorkflowTaskDependencyError(error, parsed.data) : null;
}

export class TransportError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TransportError";
  }
}

export type ContractIssueDiagnostic = Readonly<{
  code: string;
  path: readonly string[];
}>;

export class ContractError extends Error {
  readonly diagnostics: readonly ContractIssueDiagnostic[];
  readonly totalDiagnosticCount: number;

  constructor(
    message: string,
    diagnostics: readonly ContractIssueDiagnostic[] = [],
    totalDiagnosticCount = diagnostics.length,
  ) {
    const retainedDiagnostics = diagnostics.slice(0, 8);
    const completeDiagnosticCount = Math.max(totalDiagnosticCount, diagnostics.length);
    super(contractErrorMessage(message, retainedDiagnostics, completeDiagnosticCount));
    this.name = "ContractError";
    this.diagnostics = retainedDiagnostics;
    this.totalDiagnosticCount = completeDiagnosticCount;
  }
}

export type CatalogContractErrorReason =
  "malformed_response" | "project_mismatch" | "session_category_mismatch";

export class CatalogContractError extends ContractError {
  readonly reason: CatalogContractErrorReason;
  readonly method: string | null;
  readonly expectedProjectID: string | null;
  readonly actualProjectID: string | null;
  readonly expectedCategory: "main" | "subagent" | null;
  readonly actualCategory: "main" | "subagent" | null;

  private constructor(
    message: string,
    reason: CatalogContractErrorReason,
    facts: Readonly<{
      method?: string;
      expectedProjectID?: string;
      actualProjectID?: string;
      expectedCategory?: "main" | "subagent";
      actualCategory?: "main" | "subagent";
      diagnostics?: readonly ContractIssueDiagnostic[];
      totalDiagnosticCount?: number;
    }>,
  ) {
    super(message, facts.diagnostics, facts.totalDiagnosticCount);
    this.name = "CatalogContractError";
    this.reason = reason;
    this.method = facts.method ?? null;
    this.expectedProjectID = facts.expectedProjectID ?? null;
    this.actualProjectID = facts.actualProjectID ?? null;
    this.expectedCategory = facts.expectedCategory ?? null;
    this.actualCategory = facts.actualCategory ?? null;
  }

  static malformedResponse(method: string, error: ContractError): CatalogContractError {
    return new CatalogContractError(
      `${method} response did not match the catalog contract.`,
      "malformed_response",
      {
        method,
        diagnostics: error.diagnostics,
        totalDiagnosticCount: error.totalDiagnosticCount,
      },
    );
  }

  static projectMismatch(
    method: string,
    expectedProjectID: string,
    actualProjectID: string,
  ): CatalogContractError {
    return new CatalogContractError(
      `${method} response Project identity did not match the request.`,
      "project_mismatch",
      {
        method,
        expectedProjectID,
        actualProjectID,
      },
    );
  }

  static sessionCategoryMismatch(
    expectedCategory: "main" | "subagent",
    actualCategory: "main" | "subagent",
  ): CatalogContractError {
    return new CatalogContractError(
      "session.page response category did not match the request.",
      "session_category_mismatch",
      {
        method: "session.page",
        expectedCategory,
        actualCategory,
      },
    );
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

function contractErrorMessage(
  message: string,
  diagnostics: readonly ContractIssueDiagnostic[],
  totalDiagnosticCount: number,
): string {
  if (diagnostics.length === 0) {
    return message;
  }
  const retained = diagnostics
    .map((diagnostic) => `${diagnostic.path.join(".") || "<root>"} (${diagnostic.code})`)
    .join(", ");
  const omittedCount = totalDiagnosticCount - diagnostics.length;
  const omitted = omittedCount > 0 ? `, +${omittedCount.toString()} more` : "";
  return `${message} ${retained}${omitted}`;
}

import { isMessage, type ClassifiedFailure } from "@app/server-api-contract";
import { SessionAttachmentTargetDetailsSchema } from "@app/server-api-contract/gen/kent/api/connection/connection_pb";
import {
  ProjectNotFoundDetailsSchema,
  ProjectUnavailableDetailsSchema,
  WorkspaceNotRegisteredDetailsSchema,
} from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  ServerNotReadyDetailsSchema,
  ServerNotReadyReason,
} from "@app/server-api-contract/gen/kent/api/server/server_pb";
import { InternalFailureDetailsSchema } from "@app/server-api-contract/gen/kent/api/shared/foundation_pb";
import { ContractError, RpcError } from "./errors";
import type { JsonValue } from "./json";
import { rpcErrorCodes } from "./rpcErrorCodes";

type GeneratedProjectReadErrorDetail =
  | Readonly<{
      case: "projectNotFound";
      value: Readonly<{
        projectId?: string | undefined;
        sessionId?: string | undefined;
      }>;
    }>
  | Readonly<{
      case: "projectUnavailable";
      value: Readonly<{ projectId: string; rootPath: string; availability: number }>;
    }>
  | Readonly<{
      case: "workspaceNotRegistered";
      value: Readonly<{
        projectId?: string | undefined;
        workspaceId?: string | undefined;
        workspaceRoot?: string | undefined;
        sessionId?: string | undefined;
      }>;
    }>
  | Readonly<{
      case: "workspaceBindingAmbiguous";
      value: Readonly<{ canonicalRoot?: string | undefined; projectIds: readonly string[] }>;
    }>
  | Readonly<{
      case: "workspacePathIdentity";
      value: Readonly<{ workspaceRoot?: string | undefined }>;
    }>
  | Readonly<{
      case: "serverNotReady";
      value: Readonly<{
        reason: ServerNotReadyReason;
        onboardingCompleted: boolean;
        settingsPath?: string | undefined;
        diagnostic?: string | undefined;
      }>;
    }>;

type GeneratedProjectMutationErrorDetail =
  | Readonly<{ case: "projectKeyConflict"; value: Readonly<{ projectKey: string }> }>
  | Readonly<{ case: "workspaceMutationFailed"; value: Readonly<{ projectId: string; workspaceId: string }> }>
  | Readonly<{
      case: "workspaceDetachConflict";
      value: Readonly<{ projectId: string; workspaceId: string; retryable: boolean }>;
    }>
  | Readonly<{
      case: "internalFailure";
      value: Readonly<{
        operation?: string | undefined;
        cause?: string | undefined;
      }>;
    }>
  | Readonly<{
      case: "workspaceAlreadyBound" | "workspacePathMissing" | "authRequired";
      value: object;
    }>
  | Readonly<{ case: undefined; value?: undefined }>;

type GeneratedProjectErrorDetail = GeneratedProjectReadErrorDetail | GeneratedProjectMutationErrorDetail;

export type GeneratedProjectError = Readonly<{
  code: string;
  detail: GeneratedProjectErrorDetail;
}>;

export function projectRpcError(
  operation: string,
  outcome:
    | Readonly<{ case: "error"; value: GeneratedProjectError }>
    | Readonly<{ case: undefined; value?: undefined }>,
): RpcError {
  if (outcome.case !== "error") {
    return new RpcError({
      code: rpcErrorCodes.internal,
      message: `${operation} returned no outcome.`,
      method: operation,
    });
  }
  return new RpcError({
    code: projectRpcErrorCode(outcome.value.code),
    message: `${operation} failed with code ${outcome.value.code}.`,
    method: operation,
    data: projectErrorData(outcome.value),
  });
}

export function projectRpcErrorFromClassifiedFailure(
  operation: string,
  failure: ClassifiedFailure,
): RpcError {
  const detail = failure.detail;
  let generatedDetail: GeneratedProjectErrorDetail = { case: undefined };
  if (isMessage(detail, ProjectNotFoundDetailsSchema)) {
    generatedDetail = { case: "projectNotFound", value: detail };
  } else if (isMessage(detail, WorkspaceNotRegisteredDetailsSchema)) {
    generatedDetail = { case: "workspaceNotRegistered", value: detail };
  } else if (isMessage(detail, ProjectUnavailableDetailsSchema)) {
    generatedDetail = { case: "projectUnavailable", value: detail };
  } else if (isMessage(detail, ServerNotReadyDetailsSchema)) {
    generatedDetail = { case: "serverNotReady", value: detail };
  } else if (isMessage(detail, SessionAttachmentTargetDetailsSchema)) {
    const value = {
      sessionId: detail.sessionId,
      projectId: detail.projectId,
      workspaceId: detail.workspaceId,
      workspaceRoot: detail.workspaceRoot,
    };
    if (failure.code === "project_not_found") {
      generatedDetail = { case: "projectNotFound", value };
    } else if (failure.code === "workspace_not_registered") {
      generatedDetail = { case: "workspaceNotRegistered", value };
    }
  } else if (isMessage(detail, InternalFailureDetailsSchema)) {
    generatedDetail = { case: "internalFailure", value: detail };
  }
  return projectRpcError(operation, {
    case: "error",
    value: { code: failure.code, detail: generatedDetail },
  });
}

function projectRpcErrorCode(code: string): number {
  switch (code) {
    case "workspace_not_registered":
      return rpcErrorCodes.workspaceNotRegistered;
    case "project_not_found":
      return rpcErrorCodes.projectNotFound;
    case "project_unavailable":
      return rpcErrorCodes.projectUnavailable;
    case "auth_required":
      return rpcErrorCodes.authRequired;
    case "server_not_ready":
      return rpcErrorCodes.serverNotReady;
    case "workspace_path_identity":
      return rpcErrorCodes.workspacePathIdentity;
    case "workspace_detach_conflict":
      return rpcErrorCodes.workspaceDetachConflict;
    case "workspace_mutation_failed":
      return rpcErrorCodes.workspaceMutationFailed;
    default:
      return rpcErrorCodes.internal;
  }
}

function projectErrorData(error: GeneratedProjectError): JsonValue {
  const reason = error.code;
  if (isProjectReadErrorDetail(error.detail)) {
    return projectReadErrorData(reason, error.detail);
  }
  return projectMutationErrorData(reason, error.detail);
}

function isProjectReadErrorDetail(
  detail: GeneratedProjectErrorDetail,
): detail is GeneratedProjectReadErrorDetail {
  return (
    detail.case === "projectNotFound" ||
    detail.case === "projectUnavailable" ||
    detail.case === "workspaceNotRegistered" ||
    detail.case === "workspaceBindingAmbiguous" ||
    detail.case === "workspacePathIdentity" ||
    detail.case === "serverNotReady"
  );
}

function projectReadErrorData(reason: string, detail: GeneratedProjectReadErrorDetail): JsonValue {
  switch (detail.case) {
    case "projectNotFound":
      return compactProjectErrorData(reason, {
        project_id: detail.value.projectId,
        session_id: detail.value.sessionId,
      });
    case "projectUnavailable":
      return {
        reason,
        project_id: detail.value.projectId,
        root_path: detail.value.rootPath,
        availability: detail.value.availability,
      };
    case "workspaceNotRegistered":
      return compactProjectErrorData(reason, {
        project_id: detail.value.projectId,
        workspace_id: detail.value.workspaceId,
        workspace_root: detail.value.workspaceRoot,
        session_id: detail.value.sessionId,
      });
    case "workspaceBindingAmbiguous":
      return compactProjectErrorData(reason, {
        canonical_root: detail.value.canonicalRoot,
        project_ids: detail.value.projectIds,
      });
    case "workspacePathIdentity":
      return compactProjectErrorData(reason, { workspace_root: detail.value.workspaceRoot });
    case "serverNotReady":
      return {
        type: "server_not_ready",
        reason: serverNotReadyReason(detail.value.reason),
        details: compactJsonObject({
          onboarding_completed: detail.value.onboardingCompleted,
          settings_path: detail.value.settingsPath,
          diagnostic: detail.value.diagnostic,
        }),
      };
  }
}

function projectMutationErrorData(reason: string, detail: GeneratedProjectMutationErrorDetail): JsonValue {
  switch (detail.case) {
    case "projectKeyConflict":
      return { reason, project_key: detail.value.projectKey };
    case "workspaceMutationFailed":
      return {
        reason,
        project_id: detail.value.projectId,
        workspace_id: detail.value.workspaceId,
      };
    case "workspaceDetachConflict":
      return {
        reason,
        project_id: detail.value.projectId,
        workspace_id: detail.value.workspaceId,
        retryable: detail.value.retryable,
      };
    case "internalFailure":
      return compactProjectErrorData(reason, {
        operation: detail.value.operation,
        cause: detail.value.cause,
      });
    case "workspaceAlreadyBound":
    case "workspacePathMissing":
    case "authRequired":
    case undefined:
      return { reason };
  }
}

function compactProjectErrorData(
  reason: string,
  values: Readonly<Record<string, JsonValue | undefined>>,
): JsonValue {
  return Object.fromEntries([
    ["reason", reason],
    ...Object.entries(values).filter((entry): entry is [string, JsonValue] => entry[1] !== undefined),
  ]);
}

function compactJsonObject(values: Readonly<Record<string, JsonValue | undefined>>): JsonValue {
  return Object.fromEntries(
    Object.entries(values).filter((entry): entry is [string, JsonValue] => entry[1] !== undefined),
  );
}

function serverNotReadyReason(reason: ServerNotReadyReason): string {
  switch (reason) {
    case ServerNotReadyReason.ONBOARDING_REQUIRED:
      return "onboarding_required";
    case ServerNotReadyReason.ACTIVATION_FAILED:
      return "activation_failed";
    case ServerNotReadyReason.UNSPECIFIED:
      throw new ContractError(`Unsupported server-not-ready reason ${reason.toString()}.`);
  }
}

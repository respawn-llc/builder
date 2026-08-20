import { isMessage, type ClassifiedFailure } from "@app/server-api-contract";
import { SessionAttachmentTargetDetailsSchema } from "@app/server-api-contract/gen/kent/api/connection/connection_pb";
import {
  ProjectKeyConflictDetailsSchema,
  ProjectNotFoundDetailsSchema,
  ProjectUnavailableDetailsSchema,
  WorkspaceBindingAmbiguousDetailsSchema,
  WorkspaceBindingAmbiguousMutationDetailsSchema,
  WorkspaceDetachConflictDetailsSchema,
  WorkspaceMutationDetailsSchema,
  WorkspaceNotRegisteredDetailsSchema,
  WorkspacePathIdentityDetailsSchema,
} from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  ServerNotReadyDetailsSchema,
  ServerNotReadyReason,
} from "@app/server-api-contract/gen/kent/api/server/server_pb";
import { InternalFailureDetailsSchema } from "@app/server-api-contract/gen/kent/api/shared/foundation_pb";
import { ContractError, RpcError } from "./errors";
import { compactJsonObject, type JsonValue } from "./json";
import { rpcErrorCodes } from "./rpcErrorCodes";

export function projectRpcError(operation: string, failure?: ClassifiedFailure): RpcError {
  if (failure === undefined) {
    return new RpcError({
      code: rpcErrorCodes.internal,
      message: `${operation} returned no outcome.`,
      method: operation,
    });
  }
  return new RpcError({
    code: projectRpcErrorCode(failure.code),
    message: `${operation} failed with code ${failure.code}.`,
    method: operation,
    data: projectErrorData(failure.code, failure.detail),
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

function projectErrorData(reason: string, detail: ClassifiedFailure["detail"]): JsonValue {
  return (
    projectReadErrorData(reason, detail) ??
    projectMutationErrorData(reason, detail) ??
    projectConnectionErrorData(reason, detail) ?? { reason }
  );
}

function projectReadErrorData(reason: string, detail: ClassifiedFailure["detail"]): JsonValue | undefined {
  if (isMessage(detail, ProjectNotFoundDetailsSchema)) {
    return compactProjectErrorData(reason, { project_id: detail.projectId });
  }
  if (isMessage(detail, ProjectUnavailableDetailsSchema)) {
    return {
      reason,
      project_id: detail.projectId,
      root_path: detail.rootPath,
      availability: detail.availability,
    };
  }
  if (isMessage(detail, WorkspaceNotRegisteredDetailsSchema)) {
    return compactProjectErrorData(reason, {
      project_id: detail.projectId,
      workspace_id: detail.workspaceId,
      workspace_root: detail.workspaceRoot,
    });
  }
  if (isMessage(detail, WorkspaceBindingAmbiguousDetailsSchema)) {
    return {
      reason,
      canonical_root: detail.canonicalRoot,
      project_ids: detail.projectIds,
    };
  }
  if (isMessage(detail, WorkspaceBindingAmbiguousMutationDetailsSchema)) {
    return { reason, project_ids: detail.projectIds };
  }
  if (isMessage(detail, WorkspacePathIdentityDetailsSchema)) {
    return { reason, workspace_root: detail.workspaceRoot };
  }
  return undefined;
}

function projectMutationErrorData(
  reason: string,
  detail: ClassifiedFailure["detail"],
): JsonValue | undefined {
  if (isMessage(detail, ProjectKeyConflictDetailsSchema)) {
    return { reason, project_key: detail.projectKey };
  }
  if (isMessage(detail, WorkspaceMutationDetailsSchema)) {
    return {
      reason,
      project_id: detail.projectId,
      workspace_id: detail.workspaceId,
    };
  }
  if (isMessage(detail, WorkspaceDetachConflictDetailsSchema)) {
    return {
      reason,
      project_id: detail.projectId,
      workspace_id: detail.workspaceId,
      retryable: detail.retryable,
    };
  }
  return undefined;
}

function projectConnectionErrorData(
  reason: string,
  detail: ClassifiedFailure["detail"],
): JsonValue | undefined {
  if (isMessage(detail, ServerNotReadyDetailsSchema)) {
    return {
      type: "server_not_ready",
      reason: serverNotReadyReason(detail.reason),
      details: compactJsonObject({
        onboarding_completed: detail.onboardingCompleted,
        settings_path: detail.settingsPath,
        diagnostic: detail.diagnostic,
      }),
    };
  }
  if (isMessage(detail, SessionAttachmentTargetDetailsSchema)) {
    if (reason === "project_not_found") {
      return compactProjectErrorData(reason, {
        project_id: detail.projectId,
        session_id: detail.sessionId,
      });
    }
    if (reason === "workspace_not_registered") {
      return compactProjectErrorData(reason, {
        project_id: detail.projectId,
        workspace_id: detail.workspaceId,
        workspace_root: detail.workspaceRoot,
        session_id: detail.sessionId,
      });
    }
  }
  if (isMessage(detail, InternalFailureDetailsSchema)) {
    return compactProjectErrorData(reason, {
      operation: detail.operation,
      cause: detail.cause,
    });
  }
  return undefined;
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

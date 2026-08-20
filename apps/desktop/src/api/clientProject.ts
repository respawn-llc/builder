import type {
  BindingPlan,
  ProjectBinding,
  ProjectDeleteResponse,
  ProjectEdit,
  ProjectMutationResponse,
  ProjectPage,
  ProjectWorkspaceAttachResponse,
  ProjectWorkspaceResult,
  WorkspaceCatalogPage,
  WorkspaceUnlinkResponse,
} from "./models";
import { workspaceCatalogPageSize } from "./models";
import { create, operationFromDescriptor } from "@app/server-api-contract";
import {
  ProjectAvailability,
  ProjectCatalogService,
  ProjectWorkspaceAttachOutcome,
  ProjectWorkspaceGetResult,
  WorkspaceBindingPlanKind,
  WorkspaceBindingPlanMode,
  type AttachWorkspaceSuccess,
  type CreateProjectSuccess,
  type DeleteProjectSuccess,
  type ProjectBinding as GeneratedProjectBinding,
  type ProjectHomeSummary,
  type ProjectMutationBinding,
  type ProjectWorkspaceCatalogSummary,
  type SetDefaultWorkspaceSuccess,
  type UnlinkWorkspaceSuccess,
  type UpdateProjectSuccess,
} from "@app/server-api-contract/gen/kent/api/project/project_pb";
import { parseCatalogInput, requireCatalogProject } from "./clientCatalog";
import { timestampMillis } from "./clientTime";
import type { JsonValue } from "./json";
import { canonicalProjectIDSchema, workspaceOffsetSchema } from "./schemas/catalog";
import type { DescriptorRpcTransport } from "./transport";
import { CatalogContractError, ContractError, RpcError } from "./errors";
import { rpcErrorCodes } from "./rpcErrorCodes";

const listWorkspacesOperation = operationFromDescriptor(ProjectCatalogService.method.listWorkspaces).name;
const getWorkspaceOperation = operationFromDescriptor(ProjectCatalogService.method.getWorkspace).name;
const getEditOperation = operationFromDescriptor(ProjectCatalogService.method.getEdit).name;

export async function listWorkspaces(
  transport: DescriptorRpcTransport,
  projectID: string,
  offset: number,
): Promise<WorkspaceCatalogPage> {
  const validatedProjectID = parseCatalogInput(
    `${listWorkspacesOperation} project ID`,
    canonicalProjectIDSchema,
    projectID,
  );
  const validatedOffset = parseCatalogInput(
    `${listWorkspacesOperation} offset`,
    workspaceOffsetSchema,
    offset,
  );
  const method = ProjectCatalogService.method.listWorkspaces;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      projectId: validatedProjectID,
      offset: validatedOffset,
      limit: workspaceCatalogPageSize,
    }),
  );
  if (result.outcome.case !== "success") {
    throw projectRpcError(operationFromDescriptor(method).name, result.outcome);
  }
  const success = result.outcome.value;
  const response: WorkspaceCatalogPage = {
    projectID: success.projectId,
    offset: success.offset,
    workspaces: success.workspaces.map(projectWorkspaceCatalog),
    nextOffset: success.nextOffset ?? null,
  };
  requireCatalogProject(listWorkspacesOperation, validatedProjectID, response.projectID);
  if (response.offset !== validatedOffset) {
    throw CatalogContractError.malformedResponse(
      listWorkspacesOperation,
      new ContractError(
        `Response offset ${String(response.offset)} does not match ${String(validatedOffset)}.`,
      ),
    );
  }
  if (
    response.nextOffset !== null &&
    (response.workspaces.length !== workspaceCatalogPageSize ||
      response.nextOffset !== validatedOffset + workspaceCatalogPageSize)
  ) {
    throw CatalogContractError.malformedResponse(
      listWorkspacesOperation,
      new ContractError(
        `Response next offset ${String(response.nextOffset)} does not continue offset ${String(validatedOffset)} with limit ${String(workspaceCatalogPageSize)}.`,
      ),
    );
  }
  return response;
}

export async function getProjectWorkspace(
  transport: DescriptorRpcTransport,
  projectID: string,
  selector: Readonly<{ workspaceID: string } | { workspaceRoot: string }>,
): Promise<ProjectWorkspaceResult> {
  const validatedProjectID = parseCatalogInput(
    `${getWorkspaceOperation} project ID`,
    canonicalProjectIDSchema,
    projectID,
  );
  const selectorValue =
    "workspaceID" in selector
      ? { case: "workspaceId" as const, value: selector.workspaceID }
      : { case: "workspaceRoot" as const, value: selector.workspaceRoot };
  const method = ProjectCatalogService.method.getWorkspace;
  const result = await transport.callDescriptor(
    method,
    create(method.input, { projectId: validatedProjectID, selector: selectorValue }),
  );
  if (result.outcome.case !== "success") {
    throw projectRpcError(operationFromDescriptor(method).name, result.outcome);
  }
  const success = result.outcome.value;
  requireCatalogProject(getWorkspaceOperation, validatedProjectID, success.projectId);
  if (success.result === ProjectWorkspaceGetResult.ATTACHED && success.workspace !== undefined) {
    return { kind: "attached", workspace: projectWorkspaceCatalog(success.workspace) };
  }
  if (success.result === ProjectWorkspaceGetResult.NOT_ATTACHED && success.workspace === undefined) {
    return { kind: "not_attached" };
  }
  throw CatalogContractError.malformedResponse(
    getWorkspaceOperation,
    new ContractError("Workspace presence does not match the lookup result."),
  );
}

export async function getProjectEdit(
  transport: DescriptorRpcTransport,
  projectID: string,
): Promise<ProjectEdit> {
  const validatedProjectID = parseCatalogInput(
    `${getEditOperation} project ID`,
    canonicalProjectIDSchema,
    projectID,
  );
  const method = ProjectCatalogService.method.getEdit;
  const result = await transport.callDescriptor(
    method,
    create(method.input, { projectId: validatedProjectID }),
  );
  if (result.outcome.case !== "success") {
    throw projectRpcError(operationFromDescriptor(method).name, result.outcome);
  }
  requireCatalogProject(getEditOperation, validatedProjectID, result.outcome.value.projectId);
  return {
    projectID: result.outcome.value.projectId,
    projectKey: result.outcome.value.projectKey,
    displayName: result.outcome.value.displayName,
  };
}

export async function planWorkspace(transport: DescriptorRpcTransport, path: string): Promise<BindingPlan> {
  const method = ProjectCatalogService.method.planWorkspaceBinding;
  const result = await transport.callDescriptor(
    method,
    create(method.input, { path, mode: WorkspaceBindingPlanMode.INTERACTIVE }),
  );
  if (result.outcome.case !== "success") {
    throw projectRpcError(operationFromDescriptor(method).name, result.outcome);
  }
  return {
    kind: workspaceBindingPlanKind(result.outcome.value.kind),
    canonicalRoot: result.outcome.value.canonicalRoot,
    binding: result.outcome.value.binding === undefined ? null : projectBinding(result.outcome.value.binding),
  };
}

export async function listProjectHome(
  transport: DescriptorRpcTransport,
  pageToken: string,
): Promise<ProjectPage> {
  const method = ProjectCatalogService.method.listHome;
  const result = await transport.callDescriptor(
    method,
    create(method.input, { pageSize: 40, pageToken: pageToken === "" ? undefined : pageToken }),
  );
  if (result.outcome.case !== "success") {
    throw projectRpcError(operationFromDescriptor(method).name, result.outcome);
  }
  const success = result.outcome.value;
  if (success.generatedAt === undefined) {
    throw new ContractError("Project Home generated timestamp is required.");
  }
  return {
    projects: success.projects.map(projectHomeSummary),
    nextPageToken: success.nextPageToken ?? "",
    generatedAt: timestampMillis(success.generatedAt),
  };
}

export async function createProject(
  transport: DescriptorRpcTransport,
  displayName: string,
  projectKey: string,
  workspaceRoot: string,
): Promise<ProjectBinding> {
  const method = ProjectCatalogService.method.create;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      displayName,
      projectKey: projectKey === "" ? undefined : projectKey,
      workspaceRoot,
    }),
  );
  const success = projectMutationSuccess<CreateProjectSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  if (success.binding === undefined) {
    throw new ContractError("Project Create binding is required.");
  }
  return projectMutationBinding(success.binding);
}

export async function attachWorkspace(
  transport: DescriptorRpcTransport,
  projectID: string,
  workspaceRoot: string,
): Promise<ProjectWorkspaceAttachResponse> {
  const method = ProjectCatalogService.method.attachWorkspace;
  const result = await transport.callDescriptor(
    method,
    create(method.input, { projectId: projectID, workspaceRoot }),
  );
  const success = projectMutationSuccess<AttachWorkspaceSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  if (success.binding === undefined) {
    throw new ContractError("Project Workspace attach binding is required.");
  }
  return {
    binding: projectMutationBinding(success.binding),
    outcome: projectWorkspaceAttachOutcome(success.outcome),
  };
}

export async function updateProject(
  transport: DescriptorRpcTransport,
  projectID: string,
  displayName: string,
  projectKey = "",
): Promise<ProjectMutationResponse> {
  const method = ProjectCatalogService.method.update;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      projectId: projectID,
      displayName,
      projectKey: projectKey === "" ? undefined : projectKey,
    }),
  );
  const success = projectMutationSuccess<UpdateProjectSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  if (success.project === undefined) {
    throw new ContractError("Project Update summary is required.");
  }
  return { project: projectHomeSummary(success.project) };
}

export async function setDefaultWorkspace(
  transport: DescriptorRpcTransport,
  projectID: string,
  workspaceID: string,
): Promise<ProjectMutationResponse> {
  const method = ProjectCatalogService.method.setDefaultWorkspace;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      projectId: projectID,
      workspace: { selector: { case: "workspaceId", value: workspaceID } },
    }),
  );
  const success = projectMutationSuccess<SetDefaultWorkspaceSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  if (success.project === undefined) {
    throw new ContractError("Project default Workspace summary is required.");
  }
  return { project: projectHomeSummary(success.project) };
}

export async function unlinkWorkspace(
  transport: DescriptorRpcTransport,
  projectID: string,
  workspaceID: string,
): Promise<WorkspaceUnlinkResponse> {
  const method = ProjectCatalogService.method.unlinkWorkspace;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      projectId: projectID,
      workspace: { selector: { case: "workspaceId", value: workspaceID } },
    }),
  );
  const success = projectMutationSuccess<UnlinkWorkspaceSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  return {
    projectID: success.projectId,
    workspaceID: success.workspaceId,
    blockers: success.blockers.map((blocker) => ({
      code: blocker.code,
      message: workspaceUnlinkBlockerMessage(blocker.code),
      count: blocker.count ?? 0,
    })),
    project: success.project === undefined ? null : projectHomeSummary(success.project),
  };
}

export async function deleteProject(
  transport: DescriptorRpcTransport,
  projectID: string,
): Promise<ProjectDeleteResponse> {
  const method = ProjectCatalogService.method.delete;
  const result = await transport.callDescriptor(method, create(method.input, { projectId: projectID }));
  const success = projectMutationSuccess<DeleteProjectSuccess>(
    operationFromDescriptor(method).name,
    result.outcome,
  );
  return {
    projectID: success.projectId,
    deleted: success.deleted,
    blockers: success.blockers.map((blocker) => ({
      code: blocker.code,
      message: projectDeleteBlockerMessage(blocker.code),
      count: blocker.count,
    })),
  };
}

function projectWorkspaceCatalog(workspace: ProjectWorkspaceCatalogSummary) {
  return {
    id: workspace.workspaceId,
    name: workspace.displayName,
    rootPath: workspace.rootPath,
    isDefault: workspace.isDefault,
  };
}

function projectBinding(binding: GeneratedProjectBinding): ProjectBinding {
  return {
    projectID: binding.projectId,
    projectKey: binding.projectKey,
    projectName: binding.projectName,
    workspaceID: binding.workspaceId,
    canonicalRoot: binding.canonicalRoot,
    workspaceName: binding.workspaceName,
    workspaceStatus: projectAvailability(binding.workspaceStatus),
  };
}

function projectMutationBinding(binding: ProjectMutationBinding): ProjectBinding {
  return {
    projectID: binding.projectId,
    projectKey: binding.projectKey,
    projectName: binding.projectName,
    workspaceID: binding.workspaceId,
    canonicalRoot: binding.canonicalRoot,
    workspaceName: binding.workspaceName,
    workspaceStatus: projectAvailability(binding.workspaceStatus),
  };
}

function projectMutationSuccess<Success>(
  operation: string,
  outcome:
    | Readonly<{ case: "success"; value: Success }>
    | Readonly<{ case: "error"; value: GeneratedProjectError }>
    | Readonly<{ case: undefined }>,
): Success {
  if (outcome.case === "success") {
    return outcome.value;
  }
  throw projectRpcError(operation, outcome.case === "error" ? outcome : { case: undefined });
}

type GeneratedProjectReadErrorDetail =
  | Readonly<{ case: "projectNotFound"; value: Readonly<{ projectId: string }> }>
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
      }>;
    }>
  | Readonly<{
      case: "workspaceBindingAmbiguous";
      value: Readonly<{ canonicalRoot?: string | undefined; projectIds: readonly string[] }>;
    }>
  | Readonly<{
      case: "workspacePathIdentity";
      value: Readonly<{ workspaceRoot?: string | undefined }>;
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

type GeneratedProjectError = Readonly<{
  code: string;
  detail: GeneratedProjectErrorDetail;
}>;

function projectRpcError(
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
    detail.case === "workspacePathIdentity"
  );
}

function projectReadErrorData(reason: string, detail: GeneratedProjectReadErrorDetail): JsonValue {
  switch (detail.case) {
    case "projectNotFound":
      return { reason, project_id: detail.value.projectId };
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
      });
    case "workspaceBindingAmbiguous":
      return compactProjectErrorData(reason, {
        canonical_root: detail.value.canonicalRoot,
        project_ids: detail.value.projectIds,
      });
    case "workspacePathIdentity":
      return compactProjectErrorData(reason, { workspace_root: detail.value.workspaceRoot });
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

function workspaceUnlinkBlockerMessage(code: string): string {
  switch (code) {
    case "default_workspace":
      return "Workspace is the project default workspace.";
    case "non_terminal_tasks":
      return "Active or non-terminal tasks still depend on this workspace.";
    case "executable_current_nodes":
      return "Executable current nodes still depend on this workspace.";
    case "managed_owned_worktrees":
      return "Worktrees still depend on this workspace.";
    case "missing_history_snapshot":
      return "Historical task or retained Session references do not have a durable workspace path/name snapshot.";
    case "active_sessions":
      return "Active runtime sessions still depend on this workspace.";
    default:
      throw new ContractError(`Unsupported Workspace unlink blocker code ${code}.`);
  }
}

function projectDeleteBlockerMessage(code: string): string {
  switch (code) {
    case "non_terminal_tasks":
      return "Project has active or non-terminal tasks.";
    case "active_sessions":
      return "Project has active runtime sessions.";
    default:
      throw new ContractError(`Unsupported Project delete blocker code ${code}.`);
  }
}

function projectWorkspaceAttachOutcome(outcome: ProjectWorkspaceAttachOutcome) {
  switch (outcome) {
    case ProjectWorkspaceAttachOutcome.ATTACHED:
      return "attached" as const;
    case ProjectWorkspaceAttachOutcome.ALREADY_ATTACHED:
      return "already_attached" as const;
    case ProjectWorkspaceAttachOutcome.UNSPECIFIED:
      throw new ContractError("Project Workspace attach outcome is unspecified.");
  }
}

function projectHomeSummary(project: ProjectHomeSummary) {
  if (project.primaryWorkspace?.updatedAt === undefined || project.updatedAt === undefined) {
    throw new ContractError("Project Home summary is incomplete.");
  }
  return {
    id: project.projectId,
    key: project.projectKey,
    name: project.displayName,
    primaryWorkspace: {
      id: project.primaryWorkspace.workspaceId,
      name: project.primaryWorkspace.displayName,
      rootPath: project.primaryWorkspace.rootPath,
      availability: projectAvailability(project.primaryWorkspace.availability),
      isPrimary: project.primaryWorkspace.isPrimary,
      updatedAt: timestampMillis(project.primaryWorkspace.updatedAt),
    },
    defaultWorkflowID: project.defaultWorkflowId ?? null,
    defaultWorkflowName: project.defaultWorkflowName ?? "",
    defaultWorkflowValid: project.defaultWorkflowValid,
    updatedAt: timestampMillis(project.updatedAt),
    taskCount: project.taskCount,
    attentionCount: project.attentionCount,
    workflowCount: project.workflowCount,
  };
}

function projectAvailability(availability: ProjectAvailability) {
  switch (availability) {
    case ProjectAvailability.AVAILABLE:
      return "available" as const;
    case ProjectAvailability.MISSING:
      return "missing" as const;
    case ProjectAvailability.INACCESSIBLE:
      return "inaccessible" as const;
    case ProjectAvailability.UNLINKED:
      return "unlinked" as const;
    case ProjectAvailability.UNSPECIFIED:
      throw new ContractError("Project availability is unspecified.");
  }
}

function workspaceBindingPlanKind(kind: WorkspaceBindingPlanKind): string {
  switch (kind) {
    case WorkspaceBindingPlanKind.BOUND:
      return "bound";
    case WorkspaceBindingPlanKind.LOCAL_UNBOUND:
      return "local_unbound";
    case WorkspaceBindingPlanKind.SERVER_WORKSPACE_SELECTION:
      return "server_workspace_selection";
    case WorkspaceBindingPlanKind.HEADLESS_REMOTE_SELECTED:
      return "headless_remote_selected";
    case WorkspaceBindingPlanKind.HEADLESS_REMOTE_AMBIGUOUS:
      return "headless_remote_ambiguous";
    case WorkspaceBindingPlanKind.UNSPECIFIED:
      throw new ContractError("Workspace binding plan kind is unspecified.");
  }
}

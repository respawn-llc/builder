import type {
  BindingPlan,
  ProjectBinding,
  ProjectMutationBinding as ProjectMutationBindingModel,
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
import {
  classifyResult,
  create,
  OperationOutcome,
  operationFromDescriptor,
  type DescMethod,
  type Message,
} from "@app/server-api-contract";
import {
  ProjectAvailability,
  ProjectCatalogService,
  ProjectWorkspaceAttachOutcome,
  ProjectWorkspaceGetResult,
  WorkspaceBindingPlanKind,
  WorkspaceBindingPlanMode,
  type ProjectBinding as GeneratedProjectBinding,
  type ProjectHomeSummary,
  type ProjectMutationBinding,
  type ProjectWorkspaceCatalogSummary,
} from "@app/server-api-contract/gen/kent/api/project/project_pb";
import { parseCatalogInput, requireCatalogProject } from "./clientCatalog";
import { timestampMillis } from "./clientTime";
import { canonicalProjectIDSchema, workspaceOffsetSchema } from "./schemas/catalog";
import type { DescriptorRpcTransport } from "./transport";
import { CatalogContractError, ContractError } from "./errors";
import { projectRpcError } from "./projectRpcError";

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
    throw projectResultError(method, result);
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
    throw projectResultError(method, result);
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
    throw projectResultError(method, result);
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
    throw projectResultError(method, result);
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
    throw projectResultError(method, result);
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
): Promise<ProjectMutationBindingModel> {
  const method = ProjectCatalogService.method.create;
  const result = await transport.callDescriptor(
    method,
    create(method.input, {
      displayName,
      projectKey: projectKey === "" ? undefined : projectKey,
      workspaceRoot,
    }),
  );
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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
  if (result.outcome.case !== "success") {
    throw projectResultError(method, result);
  }
  const success = result.outcome.value;
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

function projectMutationBinding(binding: ProjectMutationBinding): ProjectMutationBindingModel {
  return {
    projectID: binding.projectId,
    projectKey: binding.projectKey,
    projectName: binding.projectName,
    workspaceID: binding.workspaceId,
    workspaceName: binding.workspaceName,
    workspaceStatus: projectAvailability(binding.workspaceStatus),
  };
}

function projectResultError(method: DescMethod, result: Message) {
  const operation = operationFromDescriptor(method).name;
  const classified = classifyResult(method.output, result);
  if (classified.outcome === OperationOutcome.SUCCESS) {
    return projectRpcError(operation);
  }
  return projectRpcError(operation, classified.failure);
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

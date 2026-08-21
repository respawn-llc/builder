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
import { create, operationName } from "@app/server-api-contract";
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
import { requireCatalogProject } from "./clientCatalog";
import { timestampMillis } from "./clientTime";
import { requireUnarySuccess } from "./protobufRpc";
import type { DescriptorRpcTransport } from "./transport";
import { CatalogContractError, ContractError } from "./errors";

const listWorkspacesOperation = operationName(ProjectCatalogService.method.listWorkspaces);
const getWorkspaceOperation = operationName(ProjectCatalogService.method.getWorkspace);
const getEditOperation = operationName(ProjectCatalogService.method.getEdit);

export async function listWorkspaces(
  transport: DescriptorRpcTransport,
  projectID: string,
  offset: number,
): Promise<WorkspaceCatalogPage> {
  const method = ProjectCatalogService.method.listWorkspaces;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        projectId: projectID,
        offset,
        limit: workspaceCatalogPageSize,
      }),
    ),
  );
  const response: WorkspaceCatalogPage = {
    projectID: success.projectId,
    offset: success.offset,
    workspaces: success.workspaces.map(projectWorkspaceCatalog),
    nextOffset: success.nextOffset ?? null,
  };
  requireCatalogProject(listWorkspacesOperation, projectID, response.projectID);
  if (response.offset !== offset) {
    throw CatalogContractError.malformedResponse(
      listWorkspacesOperation,
      new ContractError(`Response offset ${String(response.offset)} does not match ${String(offset)}.`),
    );
  }
  if (
    response.nextOffset !== null &&
    (response.workspaces.length !== workspaceCatalogPageSize ||
      response.nextOffset !== offset + workspaceCatalogPageSize)
  ) {
    throw CatalogContractError.malformedResponse(
      listWorkspacesOperation,
      new ContractError(
        `Response next offset ${String(response.nextOffset)} does not continue offset ${String(offset)} with limit ${String(workspaceCatalogPageSize)}.`,
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
  const selectorValue =
    "workspaceID" in selector
      ? { case: "workspaceId" as const, value: selector.workspaceID }
      : { case: "workspaceRoot" as const, value: selector.workspaceRoot };
  const method = ProjectCatalogService.method.getWorkspace;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, { projectId: projectID, selector: selectorValue }),
    ),
  );
  requireCatalogProject(getWorkspaceOperation, projectID, success.projectId);
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
  const method = ProjectCatalogService.method.getEdit;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { projectId: projectID })),
  );
  requireCatalogProject(getEditOperation, projectID, success.projectId);
  return {
    projectID: success.projectId,
    projectKey: success.projectKey,
    displayName: success.displayName,
  };
}

export async function planWorkspace(transport: DescriptorRpcTransport, path: string): Promise<BindingPlan> {
  const method = ProjectCatalogService.method.planWorkspaceBinding;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, { path, mode: WorkspaceBindingPlanMode.INTERACTIVE }),
    ),
  );
  return {
    kind: workspaceBindingPlanKind(success.kind),
    canonicalRoot: success.canonicalRoot,
    binding: success.binding === undefined ? null : projectBinding(success.binding),
  };
}

export async function listProjectHome(
  transport: DescriptorRpcTransport,
  pageToken: string | null,
): Promise<ProjectPage> {
  const method = ProjectCatalogService.method.listHome;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        pageSize: 40,
        pageToken: pageToken ?? undefined,
      }),
    ),
  );
  if (success.generatedAt === undefined) {
    throw new ContractError("Project Home generated timestamp is required.");
  }
  return {
    projects: success.projects.map(projectHomeSummary),
    nextPageToken: success.nextPageToken ?? null,
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
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        displayName,
        projectKey: projectKey === "" ? undefined : projectKey,
        workspaceRoot,
      }),
    ),
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
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { projectId: projectID, workspaceRoot })),
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
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        projectId: projectID,
        displayName,
        projectKey: projectKey === "" ? undefined : projectKey,
      }),
    ),
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
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        projectId: projectID,
        workspace: { selector: { case: "workspaceId", value: workspaceID } },
      }),
    ),
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
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        projectId: projectID,
        workspace: { selector: { case: "workspaceId", value: workspaceID } },
      }),
    ),
  );
  return {
    projectID: success.projectId,
    workspaceID: success.workspaceId,
    blockers: success.blockers.map((blocker) => ({
      code: blocker.code,
      message: workspaceUnlinkBlockerMessage(blocker.code),
      ...(blocker.count === undefined ? {} : { count: blocker.count }),
    })),
    project: success.project === undefined ? null : projectHomeSummary(success.project),
  };
}

export async function deleteProject(
  transport: DescriptorRpcTransport,
  projectID: string,
): Promise<ProjectDeleteResponse> {
  const method = ProjectCatalogService.method.delete;
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { projectId: projectID })),
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

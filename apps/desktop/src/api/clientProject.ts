import type {
  BindingPlan,
  ProjectBinding,
  ProjectDeleteResponse,
  ProjectEdit,
  ProjectMutationResponse,
  ProjectWorkspaceAttachResponse,
  ProjectWorkspaceResult,
  WorkspaceCatalogPage,
  WorkspaceUnlinkResponse,
} from "./models";
import { workspaceCatalogPageSize } from "./models";
import { parseCatalogInput, parseCatalogResponse, requireCatalogProject } from "./clientCatalog";
import { parseRpcResponse as parse } from "./clientParse";
import { canonicalProjectIDSchema, workspaceOffsetSchema } from "./schemas/catalog";
import {
  bindingPlanSchema,
  projectCreateSchema,
  projectDeleteResponseSchema,
  projectEditSchema,
  projectMutationResponseSchema,
  projectWorkspaceAttachResponseSchema,
  projectWorkspaceResultSchema,
  workspaceCatalogPageSchema,
  workspaceUnlinkResponseSchema,
} from "./schemas/project";
import type { RpcTransport } from "./transport";
import { CatalogContractError, ContractError } from "./errors";

export async function listWorkspaces(
  transport: RpcTransport,
  projectID: string,
  offset: number,
): Promise<WorkspaceCatalogPage> {
  const validatedProjectID = parseCatalogInput(
    "project.workspace.list project ID",
    canonicalProjectIDSchema,
    projectID,
  );
  const validatedOffset = parseCatalogInput("project.workspace.list offset", workspaceOffsetSchema, offset);
  const response = parseCatalogResponse(
    "project.workspace.list",
    workspaceCatalogPageSchema,
    await transport.call("project.workspace.list", {
      project_id: validatedProjectID,
      offset: validatedOffset,
      limit: workspaceCatalogPageSize,
    }),
  );
  requireCatalogProject("project.workspace.list", validatedProjectID, response.projectID);
  if (response.offset !== validatedOffset) {
    throw CatalogContractError.malformedResponse(
      "project.workspace.list",
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
      "project.workspace.list",
      new ContractError(
        `Response next offset ${String(response.nextOffset)} does not continue offset ${String(validatedOffset)} with limit ${String(workspaceCatalogPageSize)}.`,
      ),
    );
  }
  return response;
}

export async function getProjectWorkspace(
  transport: RpcTransport,
  projectID: string,
  selector: Readonly<{ workspaceID: string } | { workspaceRoot: string }>,
): Promise<ProjectWorkspaceResult> {
  const validatedProjectID = parseCatalogInput(
    "project.workspace.get project ID",
    canonicalProjectIDSchema,
    projectID,
  );
  const params =
    "workspaceID" in selector
      ? { project_id: validatedProjectID, workspace_id: selector.workspaceID }
      : { project_id: validatedProjectID, workspace_root: selector.workspaceRoot };
  const response = parseCatalogResponse(
    "project.workspace.get",
    projectWorkspaceResultSchema,
    await transport.call("project.workspace.get", params),
  );
  requireCatalogProject("project.workspace.get", validatedProjectID, response.projectID);
  return response.result;
}

export async function getProjectEdit(transport: RpcTransport, projectID: string): Promise<ProjectEdit> {
  const validatedProjectID = parseCatalogInput(
    "project.edit.get project ID",
    canonicalProjectIDSchema,
    projectID,
  );
  const response = parseCatalogResponse(
    "project.edit.get",
    projectEditSchema,
    await transport.call("project.edit.get", { project_id: validatedProjectID }),
  );
  requireCatalogProject("project.edit.get", validatedProjectID, response.projectID);
  return response;
}

export async function planWorkspace(transport: RpcTransport, path: string): Promise<BindingPlan> {
  return parse(
    "project.planWorkspaceBinding",
    bindingPlanSchema,
    await transport.call("project.planWorkspaceBinding", { path, mode: "interactive" }),
  );
}

export async function createProject(
  transport: RpcTransport,
  displayName: string,
  projectKey: string,
  workspaceRoot: string,
): Promise<ProjectBinding> {
  return parse(
    "project.create",
    projectCreateSchema,
    await transport.call("project.create", {
      display_name: displayName,
      project_key: projectKey,
      workspace_root: workspaceRoot,
    }),
  );
}

export async function attachWorkspace(
  transport: RpcTransport,
  projectID: string,
  workspaceRoot: string,
): Promise<ProjectWorkspaceAttachResponse> {
  return parse(
    "project.attachWorkspace",
    projectWorkspaceAttachResponseSchema,
    await transport.call("project.attachWorkspace", {
      project_id: projectID,
      workspace_root: workspaceRoot,
    }),
  );
}

export async function updateProject(
  transport: RpcTransport,
  projectID: string,
  displayName: string,
  projectKey = "",
): Promise<ProjectMutationResponse> {
  return parse(
    "project.update",
    projectMutationResponseSchema,
    await transport.call("project.update", {
      project_id: projectID,
      display_name: displayName,
      project_key: projectKey,
    }),
  );
}

export async function setDefaultWorkspace(
  transport: RpcTransport,
  projectID: string,
  workspaceID: string,
): Promise<ProjectMutationResponse> {
  return parse(
    "project.defaultWorkspace.set",
    projectMutationResponseSchema,
    await transport.call("project.defaultWorkspace.set", {
      project_id: projectID,
      workspace_id: workspaceID,
    }),
  );
}

export async function unlinkWorkspace(
  transport: RpcTransport,
  projectID: string,
  workspaceID: string,
): Promise<WorkspaceUnlinkResponse> {
  return parse(
    "project.unlinkWorkspace",
    workspaceUnlinkResponseSchema,
    await transport.call("project.unlinkWorkspace", {
      project_id: projectID,
      workspace_id: workspaceID,
    }),
  );
}

export async function deleteProject(
  transport: RpcTransport,
  projectID: string,
): Promise<ProjectDeleteResponse> {
  return parse(
    "project.delete",
    projectDeleteResponseSchema,
    await transport.call("project.delete", { project_id: projectID }),
  );
}

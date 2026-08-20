import { describe, expect, it } from "vitest";
import { create, operationFromDescriptor } from "@app/server-api-contract";
import {
  ProjectAvailability,
  ProjectCatalogService,
} from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  SessionCatalogService,
  SessionCategory,
} from "@app/server-api-contract/gen/kent/api/project/session_catalog_pb";
import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import { ContractError, isProjectMissingError, RpcError } from "./index";
const sessionResponse = {
  projectID: "project-1",
  category: "main" as const,
  sessions: [],
};
const sessionResult = (
  response: Readonly<{
    projectID: string;
    category: "main" | "subagent";
    sessions: readonly Readonly<{
      id: string;
      category: "main" | "subagent";
      name?: string;
      firstPromptPreview?: string;
      updatedAt: Readonly<{ seconds: bigint; nanos: number }>;
    }>[];
    nextOffset?: number;
  }>,
) =>
  create(SessionCatalogService.method.page.output, {
    outcome: {
      case: "success",
      value: {
        projectId: response.projectID,
        category: generatedSessionCategory(response.category),
        sessions: response.sessions.map((session) => ({
          sessionId: session.id,
          category: generatedSessionCategory(session.category),
          name: session.name,
          firstPromptPreview: session.firstPromptPreview,
          updatedAt: session.updatedAt,
        })),
        nextOffset: response.nextOffset,
      },
    },
  });
const generatedSessionCategory = (category: "main" | "subagent") =>
  category === "main" ? SessionCategory.MAIN : SessionCategory.SUBAGENT;
const workspaceRow = {
  workspace_id: "workspace-1",
  display_name: "Kent",
  root_path: "/workspace/kent",
  is_default: true,
} as const;
const workspaceResponse = {
  project_id: "project-1",
  offset: 0,
  workspaces: [workspaceRow],
  next_offset: 100,
} as const;
const workspaceResult = (
  response: Readonly<{
    project_id: string;
    offset: number;
    workspaces: readonly Readonly<{
      workspace_id: string;
      display_name: string;
      root_path: string;
      is_default: boolean;
    }>[];
    next_offset: number;
  }>,
) =>
  create(ProjectCatalogService.method.listWorkspaces.output, {
    outcome: {
      case: "success",
      value: {
        projectId: response.project_id,
        offset: response.offset,
        workspaces: response.workspaces.map((workspace) => ({
          workspaceId: workspace.workspace_id,
          displayName: workspace.display_name,
          rootPath: workspace.root_path,
          isDefault: workspace.is_default,
        })),
        nextOffset: response.next_offset,
      },
    },
  });
describe("ApiClient catalog boundary", () => {
  it("uses metadata-only Project Settings and mutation RPC contracts", async () => {
    const generatedProjectSummary = {
      projectId: "project-1",
      projectKey: "PROJ",
      displayName: "Project",
      primaryWorkspace: {
        workspaceId: "workspace-1",
        displayName: "Project",
        rootPath: "/tmp/project",
        availability: ProjectAvailability.AVAILABLE,
        isPrimary: true,
        updatedAt: { seconds: 1n, nanos: 0 },
      },
      defaultWorkflowId: "11111111-1111-4111-8111-111111111111",
      defaultWorkflowName: "Delivery",
      defaultWorkflowValid: true,
      updatedAt: { seconds: 1n, nanos: 0 },
      taskCount: 0,
      attentionCount: 0,
      workflowCount: 1,
    };
    const transport = new FakeRpcTransport([
      {
        descriptor: ProjectCatalogService.method.getEdit,
        result: create(ProjectCatalogService.method.getEdit.output, {
          outcome: {
            case: "success",
            value: { projectId: "project-1", projectKey: "PROJ", displayName: "Project" },
          },
        }),
      },
      {
        descriptor: ProjectCatalogService.method.update,
        result: create(ProjectCatalogService.method.update.output, {
          outcome: { case: "success", value: { project: generatedProjectSummary } },
        }),
      },
      {
        descriptor: ProjectCatalogService.method.setDefaultWorkspace,
        result: create(ProjectCatalogService.method.setDefaultWorkspace.output, {
          outcome: { case: "success", value: { project: generatedProjectSummary } },
        }),
      },
      {
        descriptor: ProjectCatalogService.method.unlinkWorkspace,
        result: create(ProjectCatalogService.method.unlinkWorkspace.output, {
          outcome: {
            case: "success",
            value: {
              projectId: "project-1",
              workspaceId: "workspace-1",
              blockers: [{ code: "default_workspace" }],
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.getProjectEdit("project-1")).resolves.toMatchObject({
      projectID: "project-1",
    });
    await client.updateProject("project-1", "Renamed");
    await client.updateProject("project-1", "Renamed", "ABC");
    await client.setDefaultWorkspace("project-1", "workspace-1");
    await expect(client.unlinkWorkspace("project-1", "workspace-1")).resolves.toMatchObject({
      blockers: [{ code: "default_workspace", count: 0 }],
    });

    expect(transport.descriptorCalls).toContainEqual({
      descriptor: ProjectCatalogService.method.getEdit,
      request: create(ProjectCatalogService.method.getEdit.input, { projectId: "project-1" }),
    });
    expect(transport.descriptorCalls).toContainEqual({
      descriptor: ProjectCatalogService.method.update,
      request: create(ProjectCatalogService.method.update.input, {
        projectId: "project-1",
        displayName: "Renamed",
      }),
    });
    expect(transport.descriptorCalls).toContainEqual({
      descriptor: ProjectCatalogService.method.update,
      request: create(ProjectCatalogService.method.update.input, {
        projectId: "project-1",
        displayName: "Renamed",
        projectKey: "ABC",
      }),
    });
    expect(transport.descriptorCalls).toContainEqual({
      descriptor: ProjectCatalogService.method.setDefaultWorkspace,
      request: create(ProjectCatalogService.method.setDefaultWorkspace.input, {
        projectId: "project-1",
        workspace: { selector: { case: "workspaceId", value: "workspace-1" } },
      }),
    });
    expect(transport.descriptorCalls).toContainEqual({
      descriptor: ProjectCatalogService.method.unlinkWorkspace,
      request: create(ProjectCatalogService.method.unlinkWorkspace.input, {
        projectId: "project-1",
        workspace: { selector: { case: "workspaceId", value: "workspace-1" } },
      }),
    });
  });

  it.each([{ category: "main" as const }, { category: "subagent" as const }])(
    "requests an explicit offset with a fixed 50-row Session page for $category",
    async ({ category }) => {
      const transport = new FakeRpcTransport([
        {
          descriptor: SessionCatalogService.method.page,
          result: sessionResult({ ...sessionResponse, category, nextOffset: 100 }),
        },
      ]);
      const client = new ApiClient(transport);

      await expect(client.listSessionPage("project-1", category, 50)).resolves.toMatchObject({
        projectID: "project-1",
        category,
        nextOffset: 100,
      });
      expect(transport.descriptorCalls).toEqual([
        {
          descriptor: SessionCatalogService.method.page,
          request: create(SessionCatalogService.method.page.input, {
            projectId: "project-1",
            category: generatedSessionCategory(category),
            offset: 50,
            limit: 50,
          }),
        },
      ]);
    },
  );
  it.each([-1, 0.5, Number.NaN])("rejects invalid Session offset %j", async (offset) => {
    const transport = new FakeRpcTransport([]);
    await expect(
      new ApiClient(transport).listSessionPage("project-1", "main", offset),
    ).rejects.toBeInstanceOf(ContractError);
    expect(transport.descriptorCalls).toHaveLength(0);
  });
  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])(
    "rejects invalid workspace offsets %j",
    async (offset) => {
      const transport = new FakeRpcTransport([]);
      await expect(new ApiClient(transport).listWorkspaces("project-1", offset)).rejects.toBeInstanceOf(
        ContractError,
      );
      expect(transport.calls).toHaveLength(0);
    },
  );
  it.each(["", " ", " project-1", "project-1 "])(
    "rejects invalid catalog Project IDs %j",
    async (projectID) => {
      const transport = new FakeRpcTransport([]);
      const client = new ApiClient(transport);
      await expect(client.listSessionPage(projectID, "main", 0)).rejects.toBeInstanceOf(ContractError);
      await expect(client.listWorkspaces(projectID, 0)).rejects.toBeInstanceOf(ContractError);
      expect(transport.calls).toHaveLength(0);
    },
  );
  it("maps omitted Session next offset to null", async () => {
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          { descriptor: SessionCatalogService.method.page, result: sessionResult(sessionResponse) },
        ]),
      ).listSessionPage("project-1", "main", 0),
    ).resolves.toMatchObject({ nextOffset: null });
  });
  it("validates Session page identity and preserves RPC failures", async () => {
    const projectMismatch = new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: SessionCatalogService.method.page,
          result: sessionResult({ ...sessionResponse, projectID: "project-2" }),
        },
      ]),
    );
    await expect(projectMismatch.listSessionPage("project-1", "main", 0)).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
    const categoryMismatch = new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: SessionCatalogService.method.page,
          result: sessionResult({ ...sessionResponse, category: "subagent" }),
        },
      ]),
    );
    await expect(categoryMismatch.listSessionPage("project-1", "main", 0)).rejects.toMatchObject({
      reason: "session_category_mismatch",
      expectedCategory: "main",
      actualCategory: "subagent",
    });
    const operation = operationFromDescriptor(SessionCatalogService.method.page).name;
    const rpcError = new RpcError({ code: -32000, message: "unavailable", method: operation });
    const failed = new ApiClient(
      new FakeRpcTransport([{ descriptor: SessionCatalogService.method.page, error: rpcError }]),
    );
    await expect(failed.listSessionPage("project-1", "main", 0)).rejects.toBe(rpcError);
  });
  it("validates the numeric-offset workspace page", async () => {
    const fullWorkspaceResponse = {
      ...workspaceResponse,
      workspaces: Array.from({ length: 100 }, (_, index) => ({
        ...workspaceRow,
        workspace_id: `workspace-${String(index)}`,
      })),
    };
    const transport = new FakeRpcTransport([
      {
        descriptor: ProjectCatalogService.method.listWorkspaces,
        result: workspaceResult(fullWorkspaceResponse),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listWorkspaces("project-1", 0)).resolves.toMatchObject({
      projectID: "project-1",
      offset: 0,
      nextOffset: 100,
    });
    expect(transport.descriptorCalls).toEqual([
      {
        descriptor: ProjectCatalogService.method.listWorkspaces,
        request: create(ProjectCatalogService.method.listWorkspaces.input, {
          projectId: "project-1",
          offset: 0,
          limit: 100,
        }),
      },
    ]);
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          {
            descriptor: ProjectCatalogService.method.listWorkspaces,
            result: workspaceResult({ ...workspaceResponse, project_id: "project-2" }),
          },
        ]),
      ).listWorkspaces("project-1", 0),
    ).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });

    const missingProjectResult = (
      descriptor:
        typeof ProjectCatalogService.method.listWorkspaces | typeof ProjectCatalogService.method.getWorkspace,
    ) =>
      create(descriptor.output, {
        outcome: {
          case: "error",
          value: {
            code: "project_not_found",
            detail: {
              case: "projectNotFound",
              value: { projectId: "project-1" },
            },
          },
        },
      });
    const missingCatalog = await new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: ProjectCatalogService.method.listWorkspaces,
          result: missingProjectResult(ProjectCatalogService.method.listWorkspaces),
        },
      ]),
    )
      .listWorkspaces("project-1", 0)
      .catch((error: unknown) => error);
    expect(missingCatalog).toBeInstanceOf(RpcError);
    expect(isProjectMissingError(missingCatalog)).toBe(true);

    const missingExact = await new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: ProjectCatalogService.method.getWorkspace,
          result: missingProjectResult(ProjectCatalogService.method.getWorkspace),
        },
      ]),
    )
      .getProjectWorkspace("project-1", { workspaceID: "workspace-1" })
      .catch((error: unknown) => error);
    expect(missingExact).toBeInstanceOf(RpcError);
    expect(isProjectMissingError(missingExact)).toBe(true);
  });
  it.each([
    {
      name: "short page continuation",
      response: workspaceResponse,
    },
    {
      name: "non-progressing continuation",
      response: {
        ...workspaceResponse,
        workspaces: Array.from({ length: 100 }, (_, index) => ({
          ...workspaceRow,
          workspace_id: `workspace-${String(index)}`,
        })),
        next_offset: 50,
      },
    },
  ])("rejects $name in a Workspace page", async ({ response }) => {
    const client = new ApiClient(
      new FakeRpcTransport([
        { descriptor: ProjectCatalogService.method.listWorkspaces, result: workspaceResult(response) },
      ]),
    );
    await expect(client.listWorkspaces("project-1", 0)).rejects.toMatchObject({
      reason: "malformed_response",
    });
  });
  it("rejects malformed successful catalog payloads", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: SessionCatalogService.method.page,
          result: sessionResult({
            ...sessionResponse,
            sessions: Array.from({ length: 51 }, (_, index) => ({
              id: `session-${String(index)}`,
              category: "main",
              updatedAt: { seconds: 1n, nanos: 0 },
            })),
          }),
        },
      ]),
    );
    await expect(client.listSessionPage("project-1", "main", 0)).rejects.toBeDefined();
  });
});

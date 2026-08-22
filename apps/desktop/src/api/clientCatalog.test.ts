import { describe, expect, it } from "vitest";
import { create } from "@app/server-api-contract";
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

describe("ApiClient catalog boundary", () => {
  it.each([
    { category: SessionCategory.MAIN, expected: "main" as const },
    { category: SessionCategory.SUBAGENT, expected: "subagent" as const },
  ])("projects a $expected Session page", async ({ category, expected }) => {
    const client = clientWith(
      SessionCatalogService.method.page,
      sessionResult({ category, nextOffset: 100 }),
    );

    await expect(client.listSessionPage("project-1", expected, 50)).resolves.toMatchObject({
      projectID: "project-1",
      category: expected,
      sessions: [],
      nextOffset: 100,
    });
  });

  it("rejects a Session page for another Project", async () => {
    const client = clientWith(SessionCatalogService.method.page, sessionResult({ projectId: "project-2" }));

    await expect(client.listSessionPage("project-1", "main", 0)).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
  });

  it("projects terminal Project Home and full Workspace pages", async () => {
    const terminalProjectPage = clientWith(
      ProjectCatalogService.method.listHome,
      create(ProjectCatalogService.method.listHome.output, {
        outcome: {
          case: "success",
          value: {
            projects: [
              {
                projectId: "project-1",
                projectKey: "PRJ",
                displayName: "Project",
                primaryWorkspace: {
                  workspaceId: "workspace-1",
                  displayName: "Workspace",
                  rootPath: "/workspace",
                  availability: ProjectAvailability.AVAILABLE,
                  isPrimary: true,
                  updatedAt: { seconds: 1n, nanos: 0 },
                },
                updatedAt: { seconds: 1n, nanos: 0 },
              },
            ],
            generatedAt: { seconds: 1n, nanos: 0 },
          },
        },
      }),
    );
    await expect(terminalProjectPage.listProjects(null)).resolves.toMatchObject({
      projects: [{ defaultWorkflowID: null, defaultWorkflowName: null }],
      nextPageToken: null,
    });

    const client = clientWith(
      ProjectCatalogService.method.listWorkspaces,
      workspaceResult({ workspaceCount: 100, nextOffset: 100 }),
    );

    const page = await client.listWorkspaces("project-1", 0);
    expect(page).toMatchObject({
      projectID: "project-1",
      offset: 0,
      nextOffset: 100,
    });
    expect(page.workspaces[0]).toMatchObject({ id: "workspace-0", rootPath: "/workspace/0" });
  });

  it.each([
    { workspaceCount: 1, nextOffset: 100 },
    { workspaceCount: 100, nextOffset: 50 },
  ])("rejects a malformed Workspace continuation %#", async (response) => {
    const client = clientWith(ProjectCatalogService.method.listWorkspaces, workspaceResult(response));

    await expect(client.listWorkspaces("project-1", 0)).rejects.toMatchObject({
      reason: "malformed_response",
    });
  });

  it("preserves typed protobuf failures as RpcError", async () => {
    const client = clientWith(
      ProjectCatalogService.method.listWorkspaces,
      create(ProjectCatalogService.method.listWorkspaces.output, {
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
      }),
    );

    const error = await client.listWorkspaces("project-1", 0).catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(RpcError);
    expect(isProjectMissingError(error)).toBe(true);
    expect(error).toMatchObject({
      method: "kent.api.project.project_catalog_service.list_workspaces",
      data: { code: "project_not_found" },
    });

    const malformedClient = clientWith(
      ProjectCatalogService.method.listWorkspaces,
      create(ProjectCatalogService.method.listWorkspaces.output, {
        outcome: {
          case: "error",
          value: {
            code: "internal_failure",
            detail: {
              case: "projectNotFound",
              value: { projectId: "project-1" },
            },
          },
        },
      }),
    );
    await expect(malformedClient.listWorkspaces("project-1", 0)).rejects.toBeInstanceOf(ContractError);
  });
});

function clientWith(
  descriptor:
    | typeof SessionCatalogService.method.page
    | typeof ProjectCatalogService.method.listHome
    | typeof ProjectCatalogService.method.listWorkspaces,
  result: ReturnType<typeof create>,
): ApiClient {
  return new ApiClient(new FakeRpcTransport([{ descriptor, result }]));
}

function sessionResult(
  response: Readonly<{
    projectId?: string;
    category?: SessionCategory;
    nextOffset?: number;
  }> = {},
) {
  return create(SessionCatalogService.method.page.output, {
    outcome: {
      case: "success",
      value: {
        projectId: response.projectId ?? "project-1",
        category: response.category ?? SessionCategory.MAIN,
        sessions: [],
        ...(response.nextOffset === undefined ? {} : { nextOffset: response.nextOffset }),
      },
    },
  });
}

function workspaceResult(response: Readonly<{ workspaceCount: number; nextOffset: number }>) {
  return create(ProjectCatalogService.method.listWorkspaces.output, {
    outcome: {
      case: "success",
      value: {
        projectId: "project-1",
        offset: 0,
        workspaces: Array.from({ length: response.workspaceCount }, (_, index) => ({
          workspaceId: `workspace-${String(index)}`,
          displayName: `Workspace ${String(index)}`,
          rootPath: `/workspace/${String(index)}`,
          isDefault: index === 0,
        })),
        nextOffset: response.nextOffset,
      },
    },
  });
}

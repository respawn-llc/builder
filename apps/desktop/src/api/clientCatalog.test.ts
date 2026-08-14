import { describe, expect, it } from "vitest";
import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import { CatalogContractError, ContractError, RpcError } from "./index";
const sessionResponse = {
  project_id: "project-1",
  category: "main",
  sessions: [],
} as const;
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
describe("ApiClient catalog boundary", () => {
  it.each([{ category: "main" as const }, { category: "subagent" as const }])(
    "requests an explicit offset with a fixed 50-row Session page for $category",
    async ({ category }) => {
      const transport = new FakeRpcTransport([
        { method: "session.page", result: { ...sessionResponse, category, next_offset: 100 } },
      ]);
      const client = new ApiClient(transport);

      await expect(client.listSessionPage("project-1", category, 50)).resolves.toMatchObject({
        projectID: "project-1",
        category,
        nextOffset: 100,
      });
      expect(transport.calls).toEqual([
        {
          method: "session.page",
          params: {
            project_id: "project-1",
            category,
            offset: 50,
            limit: 50,
          },
        },
      ]);
    },
  );
  it.each([-1, 0.5, Number.NaN])("rejects invalid Session offset %j", async (offset) => {
    const transport = new FakeRpcTransport([]);
    await expect(
      new ApiClient(transport).listSessionPage("project-1", "main", offset),
    ).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])("rejects invalid workspace offsets %j", async (offset) => {
    const transport = new FakeRpcTransport([]);
    await expect(new ApiClient(transport).listWorkspaces("project-1", offset)).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it.each(["", " ", " project-1", "project-1 "])("rejects invalid catalog Project IDs %j", async (projectID) => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    await expect(client.listSessionPage(projectID, "main", 0)).rejects.toBeInstanceOf(ContractError);
    await expect(client.listWorkspaces(projectID, 0)).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it.each([
    { name: "omitted", response: sessionResponse },
    { name: "null", response: { ...sessionResponse, next_offset: null } },
  ])("maps $name Session next offset to null", async ({ response }) => {
    await expect(
      new ApiClient(new FakeRpcTransport([{ method: "session.page", result: response }])).listSessionPage(
        "project-1",
        "main",
        0,
      ),
    ).resolves.toMatchObject({ nextOffset: null });
  });
  it("validates Session page identity and preserves RPC failures", async () => {
    const projectMismatch = new ApiClient(
      new FakeRpcTransport([
        { method: "session.page", result: { ...sessionResponse, project_id: "project-2" } },
      ]),
    );
    await expect(projectMismatch.listSessionPage("project-1", "main", 0)).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
    const categoryMismatch = new ApiClient(
      new FakeRpcTransport([
        { method: "session.page", result: { ...sessionResponse, category: "subagent" } },
      ]),
    );
    await expect(categoryMismatch.listSessionPage("project-1", "main", 0)).rejects.toMatchObject({
      reason: "session_category_mismatch",
      expectedCategory: "main",
      actualCategory: "subagent",
    });
    const rpcError = new RpcError({ code: -32000, message: "unavailable", method: "session.page" });
    const failed = new ApiClient(new FakeRpcTransport([{ method: "session.page", error: rpcError }]));
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
      { method: "project.workspace.list", result: fullWorkspaceResponse },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listWorkspaces("project-1", 0)).resolves.toMatchObject({
      projectID: "project-1",
      offset: 0,
      nextOffset: 100,
    });
    expect(transport.calls).toEqual([
      {
        method: "project.workspace.list",
        params: { project_id: "project-1", offset: 0, limit: 100 },
      },
    ]);
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          { method: "project.workspace.list", result: { ...workspaceResponse, project_id: "project-2" } },
        ]),
      ).listWorkspaces("project-1", 0),
    ).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
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
      new FakeRpcTransport([{ method: "project.workspace.list", result: response }]),
    );
    await expect(client.listWorkspaces("project-1", 0)).rejects.toMatchObject({
      reason: "malformed_response",
    });
  });
  it("reports malformed successful catalog payloads as typed errors", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "session.page",
          result: {
            ...sessionResponse,
            sessions: Array.from({ length: 51 }, (_, index) => ({
              session_id: `session-${String(index)}`,
              category: "main",
              updated_at: "2026-08-09T10:00:00Z",
            })),
          },
        },
      ]),
    );
    const error = await catchError(client.listSessionPage("project-1", "main", 0));
    expect(error).toBeInstanceOf(CatalogContractError);
    expect(error).toBeInstanceOf(ContractError);
    expect(error).toMatchObject({ reason: "malformed_response" });
  });
  it.each([
    {
      name: "old only",
      result: { ...sessionResponse, older: "opaque" },
    },
    {
      name: "mixed continuation and offset",
      result: { ...sessionResponse, next_offset: 50, newer: "opaque" },
    },
  ])("rejects $name Session response fields", async ({ result }) => {
    const client = new ApiClient(new FakeRpcTransport([{ method: "session.page", result }]));
    await expect(client.listSessionPage("project-1", "main", 0)).rejects.toBeInstanceOf(CatalogContractError);
  });
  it("preserves the complete diagnostic count when wrapping malformed responses", () => {
    const source = new ContractError(
      "source contract failure",
      Array.from({ length: 12 }, (_, index) => ({
        code: "invalid_type",
        path: [`field-${String(index)}`],
      })),
    );

    const error = CatalogContractError.malformedResponse("session.page", source);

    expect(error.diagnostics).toHaveLength(8);
    expect(error.totalDiagnosticCount).toBe(12);
    expect(error.message).toContain("+4 more");
  });
});
async function catchError(operation: Promise<unknown>): Promise<unknown> {
  try {
    await operation;
  } catch (error) {
    return error;
  }
  throw new Error("operation did not fail");
}

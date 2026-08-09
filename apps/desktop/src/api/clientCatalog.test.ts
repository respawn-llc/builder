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
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
} as const;
const workspaceResponse = {
  project_id: "project-1",
  workspaces: [workspaceRow],
  default_workspace_id: "workspace-1",
  next_page_token: "next-token",
} as const;
describe("ApiClient catalog boundary", () => {
  it.each([
    { category: "main" as const },
    { category: "subagent" as const },
  ])("requests a fixed 100-row newest Session page for $category", async ({ category }) => {
    const transport = new FakeRpcTransport([
      { method: "session.page", result: { ...sessionResponse, category } },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listSessionPage("project-1", category, { kind: "newest" })).resolves.toMatchObject({
      projectID: "project-1",
      category,
    });
    expect(transport.calls).toEqual([
      {
        method: "session.page",
        params: {
          project_id: "project-1",
          category,
          page_size: 100,
          position: { kind: "newest" },
        },
      },
    ]);
  });
  it.each([
    { kind: "older" as const, token: "opaque-older" },
    { kind: "newer" as const, token: "opaque-newer" },
  ])("encodes opaque $kind Session positions", async (position) => {
    const transport = new FakeRpcTransport([{ method: "session.page", result: sessionResponse }]);
    await new ApiClient(transport).listSessionPage("project-1", "main", position);
    expect(transport.calls[0]?.params).toMatchObject({ position });
  });
  it.each([
    { kind: "older" as const, token: "" },
    { kind: "older" as const, token: " token" },
    { kind: "newer" as const, token: "token " },
  ])("rejects malformed Session continuation $kind", async (position) => {
    const transport = new FakeRpcTransport([]);
    await expect(
      new ApiClient(transport).listSessionPage("project-1", "main", position),
    ).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it.each([" ", " next-token", "next-token "])("rejects invalid workspace page tokens %j", async (pageToken) => {
    const transport = new FakeRpcTransport([]);
    await expect(new ApiClient(transport).listWorkspaces("project-1", pageToken)).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it.each(["", " ", " project-1", "project-1 "])("rejects invalid catalog Project IDs %j", async (projectID) => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    await expect(client.listSessionPage(projectID, "main", { kind: "newest" })).rejects.toBeInstanceOf(ContractError);
    await expect(client.listWorkspaces(projectID)).rejects.toBeInstanceOf(ContractError);
    expect(transport.calls).toHaveLength(0);
  });
  it("validates Session page identity and preserves RPC failures", async () => {
    const projectMismatch = new ApiClient(
      new FakeRpcTransport([{ method: "session.page", result: { ...sessionResponse, project_id: "project-2" } }]),
    );
    await expect(projectMismatch.listSessionPage("project-1", "main", { kind: "newest" })).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
    const categoryMismatch = new ApiClient(
      new FakeRpcTransport([{ method: "session.page", result: { ...sessionResponse, category: "subagent" } }]),
    );
    await expect(categoryMismatch.listSessionPage("project-1", "main", { kind: "newest" })).rejects.toMatchObject({
      reason: "session_category_mismatch",
      expectedCategory: "main",
      actualCategory: "subagent",
    });
    const rpcError = new RpcError({ code: -32000, message: "unavailable", method: "session.page" });
    const failed = new ApiClient(new FakeRpcTransport([{ method: "session.page", error: rpcError }]));
    await expect(failed.listSessionPage("project-1", "main", { kind: "newest" })).rejects.toBe(rpcError);
  });
  it("validates the existing workspace page in place", async () => {
    const transport = new FakeRpcTransport([{ method: "project.workspace.list", result: workspaceResponse }]);
    const client = new ApiClient(transport);

    await expect(client.listWorkspaces("project-1")).resolves.toMatchObject({
      projectID: "project-1",
      nextPageToken: "next-token",
    });
    expect(transport.calls).toEqual([
      {
        method: "project.workspace.list",
        params: { project_id: "project-1", page_size: 100, page_token: "" },
      },
    ]);
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          { method: "project.workspace.list", result: { ...workspaceResponse, project_id: "project-2" } },
        ]),
      ).listWorkspaces("project-1"),
    ).rejects.toMatchObject({
      reason: "project_mismatch",
      expectedProjectID: "project-1",
      actualProjectID: "project-2",
    });
  });
  it("normalizes the existing workspace terminal continuation to null", async () => {
    await expect(
      new ApiClient(
        new FakeRpcTransport([{ method: "project.workspace.list", result: { ...workspaceResponse } }]),
      ).listWorkspaces("project-1"),
    ).resolves.toMatchObject({ nextPageToken: "next-token" });
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          {
            method: "project.workspace.list",
            result: { ...workspaceResponse, next_page_token: "" },
          },
        ]),
      ).listWorkspaces("project-1"),
    ).resolves.toMatchObject({ nextPageToken: null });
    await expect(
      new ApiClient(
        new FakeRpcTransport([
          {
            method: "project.workspace.list",
            result: { ...workspaceResponse, next_page_token: undefined },
          },
        ]),
      ).listWorkspaces("project-1"),
    ).resolves.toMatchObject({ nextPageToken: null });
  });
  it("reports malformed successful catalog payloads as typed errors", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "session.page",
          result: {
            ...sessionResponse,
            sessions: Array.from({ length: 101 }, (_, index) => ({
              session_id: `session-${String(index)}`,
              category: "main",
              updated_at: "2026-08-09T10:00:00Z",
            })),
          },
        },
      ]),
    );
    const error = await catchError(client.listSessionPage("project-1", "main", { kind: "newest" }));
    expect(error).toBeInstanceOf(CatalogContractError);
    expect(error).toBeInstanceOf(ContractError);
    expect(error).toMatchObject({ reason: "malformed_response" });
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

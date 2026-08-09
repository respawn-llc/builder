import { describe, expect, it } from "vitest";
import { sessionPageResponseSchema } from "./catalog";
import { workspaceListSchema } from "./project";
const session = {
  session_id: "session-1",
  category: "main",
  first_prompt_preview: "Build the catalog",
  updated_at: "2026-08-09T10:00:00Z",
} as const;
const workspace = {
  workspace_id: "workspace-1",
  display_name: "Kent",
  root_path: "/workspace/kent",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
} as const;
describe("Session catalog schema", () => {
  it.each([
    { name: undefined, expectedName: null },
    { name: null, expectedName: null },
    { name: "Catalog session", expectedName: "Catalog session" },
  ])("maps Session name $name to its nullable domain form", ({ name, expectedName }) => {
    const value = sessionPageResponseSchema.parse({
      project_id: "project-1",
      category: "main",
      sessions: [{ ...session, ...(name === undefined ? {} : { name }) }],
      older: "older-token",
    });

    expect(value.sessions[0]).toEqual({
      id: "session-1",
      category: "main",
      name: expectedName,
      firstPromptPreview: "Build the catalog",
      updatedAt: Date.parse("2026-08-09T10:00:00Z"),
    });
  });
  it("maps omitted prompt previews to null and preserves present previews", () => {
    expect(
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, first_prompt_preview: undefined }],
      }).sessions[0]?.firstPromptPreview,
    ).toBeNull();
    expect(
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, first_prompt_preview: "  preview  " }],
      }).sessions[0]?.firstPromptPreview,
    ).toBe("  preview  ");
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, first_prompt_preview: null }],
      }),
    ).toThrow();
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [],
        older: null,
      }),
    ).toThrow();
  });
  it.each(["", " ", "\t"])("rejects invalid present Session names %j", (name) => {
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, name }],
      }),
    ).toThrow();
  });
  it.each(["", " project-1", "project-1 "])("rejects non-canonical Project IDs %j", (projectID) => {
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: projectID,
        category: "main",
        sessions: [],
      }),
    ).toThrow();
  });
  it.each(["", " session-1", "session-1 "])("rejects non-canonical Session IDs %j", (sessionID) => {
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, session_id: sessionID }],
      }),
    ).toThrow();
  });
  it.each(["1970-01-01T00:00:00Z", "1969-12-31T23:59:59Z", "not-a-timestamp"])(
    "rejects invalid Session recency %j",
    (updatedAt) => {
      expect(() =>
        sessionPageResponseSchema.parse({
          project_id: "project-1",
          category: "main",
          sessions: [{ ...session, updated_at: updatedAt }],
        }),
      ).toThrow();
    },
  );
  it("rejects row categories that differ from the page category and pages above 100 rows", () => {
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [{ ...session, category: "subagent" }],
      }),
    ).toThrow();
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: Array.from({ length: 101 }, (_, index) => ({
          ...session,
          session_id: `session-${String(index)}`,
        })),
      }),
    ).toThrow();
  });
});
describe("Workspace list schema", () => {
  const page = {
    project_id: "project-1",
    workspaces: [workspace],
    default_workspace_id: "workspace-1",
  };
  it("preserves an empty display name for a filesystem-root workspace", () => {
    expect(
      workspaceListSchema.parse({
        ...page,
        workspaces: [{ ...workspace, display_name: "", root_path: "/" }],
        next_page_token: "",
      }).workspaces[0],
    ).toMatchObject({ id: "workspace-1", name: "", rootPath: "/" });
  });
  it.each([
    ["project_id", ""],
    ["project_id", " "],
    ["project_id", " project-1"],
    ["project_id", "project-1 "],
    ["workspace_id", ""],
    ["workspace_id", " "],
    ["workspace_id", " workspace-1"],
    ["workspace_id", "workspace-1 "],
    ["default_workspace_id", ""],
    ["default_workspace_id", " "],
    ["default_workspace_id", " workspace-1"],
    ["default_workspace_id", "workspace-1 "],
  ])("rejects invalid required ID %s", (field, invalid) => {
    const value = { ...page, workspaces: [workspace], [field]: invalid };
    if (field === "workspace_id") {
      expect(() =>
        workspaceListSchema.parse({
          ...page,
          workspaces: [{ ...workspace, workspace_id: invalid }],
        }),
      ).toThrow();
      return;
    }
    expect(() => workspaceListSchema.parse(value)).toThrow();
  });
  it("rejects empty required workspace roots", () => {
    expect(() =>
      workspaceListSchema.parse({
        ...page,
        workspaces: [{ ...workspace, root_path: "" }],
      }),
    ).toThrow();
  });
  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, "1"])(
    "rejects invalid workspace recency %j",
    (updatedAt) => {
      expect(() =>
        workspaceListSchema.parse({
          ...page,
          workspaces: [{ ...workspace, updated_at_unix_ms: updatedAt }],
        }),
      ).toThrow();
    },
  );
  it("normalizes omitted and exact-empty terminal continuations", () => {
    expect(workspaceListSchema.parse(page).nextPageToken).toBeNull();
    expect(workspaceListSchema.parse({ ...page, next_page_token: "" }).nextPageToken).toBeNull();
    expect(workspaceListSchema.parse({ ...page, next_page_token: "next-token" }).nextPageToken).toBe(
      "next-token",
    );
  });

  it.each([" ", "\tnext-token", "next-token "])("rejects malformed nonterminal continuation %j", (token) => {
    expect(() => workspaceListSchema.parse({ ...page, next_page_token: token })).toThrow();
  });

  it("rejects workspace pages above 100 rows", () => {
    expect(() =>
      workspaceListSchema.parse({
        ...page,
        workspaces: Array.from({ length: 101 }, (_, index) => ({
          ...workspace,
          workspace_id: `workspace-${String(index)}`,
        })),
      }),
    ).toThrow();
  });
});

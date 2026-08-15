import { describe, expect, it } from "vitest";
import { sessionPageResponseSchema } from "./catalog";
import { workspaceCatalogPageSchema } from "./project";
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
  is_default: true,
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
      next_offset: 50,
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
        older: "obsolete",
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
  it("rejects row categories that differ from the page category and pages above 50 rows", () => {
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
        sessions: Array.from({ length: 51 }, (_, index) => ({
          ...session,
          session_id: `session-${String(index)}`,
        })),
      }),
    ).toThrow();
  });
  it.each([0, -1, 1.5])("rejects invalid next offsets %j", (nextOffset) => {
    expect(() =>
      sessionPageResponseSchema.parse({
        project_id: "project-1",
        category: "main",
        sessions: [],
        next_offset: nextOffset,
      }),
    ).toThrow();
  });
  it.each([{ older: "opaque" }, { newer: "opaque", next_offset: 50 }])(
    "rejects obsolete Session continuation fields",
    (fields) => {
      expect(() =>
        sessionPageResponseSchema.parse({
          project_id: "project-1",
          category: "main",
          sessions: [],
          ...fields,
        }),
      ).toThrow();
    },
  );
});
describe("Workspace catalog schema", () => {
  const page = {
    project_id: "project-1",
    offset: 0,
    workspaces: [workspace],
    next_offset: null,
  };
  it("preserves numeric offsets and nullable continuation", () => {
    expect(workspaceCatalogPageSchema.parse(page)).toMatchObject({ offset: 0, nextOffset: null });
  });

  it("rejects obsolete catalog fields", () => {
    expect(() =>
      workspaceCatalogPageSchema.parse({ ...page, default_workspace_id: "workspace-1" }),
    ).toThrow();
    expect(() => workspaceCatalogPageSchema.parse({ ...page, next_page_token: "" })).toThrow();
  });
});

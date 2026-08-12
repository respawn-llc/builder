import type { WorkspaceCatalogPage, WorkspaceCatalogRow } from "@/api";

import {
  projectWorkspaceSelectorProjection,
  type WorkspaceSelectionState,
  updateWorkspaceSelection,
} from "./workspaceSelectorProjection";

const workspace = (id: string, name = id, isDefault = false): WorkspaceCatalogRow => ({
  id,
  name,
  rootPath: `/${id}`,
  isDefault,
});
const page = (offset: number, workspaces: readonly WorkspaceCatalogRow[]): WorkspaceCatalogPage => ({
  projectID: "project-1",
  offset,
  workspaces,
  nextOffset: null,
});
const initial = (initiating: WorkspaceSelectionState["initiating"]): WorkspaceSelectionState => ({
  catalog: { state: "pending" },
  initiating,
  selection: { state: "uncommitted" },
});

describe("Project Workspace selector projection", () => {
  it("uses initiating, retained selection, and first loaded occurrence precedence", () => {
    const projection = projectWorkspaceSelectorProjection({
      catalogPages: [
        page(0, [workspace("same", "first"), workspace("loaded")]),
        page(100, [workspace("same", "duplicate")]),
      ],
      initiatingRow: workspace("initiating"),
      selectedSnapshot: workspace("selected"),
      catalogExhausted: true,
    });
    expect(projection.rows.map(({ id }) => id)).toEqual(["initiating", "selected", "same", "loaded"]);
    expect(projection.rows.find(({ id }) => id === "same")?.name).toBe("first");
  });

  it("replaces a retained selected snapshot with a freshly loaded row", () => {
    expect(
      projectWorkspaceSelectorProjection({
        catalogPages: [page(0, [workspace("selected", "fresh")])],
        initiatingRow: undefined,
        selectedSnapshot: workspace("selected", "retained"),
        catalogExhausted: true,
      }).rows,
    ).toEqual([workspace("selected", "fresh")]);
  });

  it("disables selection only for one identity in an exhausted catalog", () => {
    const project = (catalogExhausted: boolean, committed = true) =>
      projectWorkspaceSelectorProjection({
        catalogPages: [page(0, [workspace("only")])],
        initiatingRow: undefined,
        selectedSnapshot: committed ? workspace("only") : undefined,
        catalogExhausted,
      }).selectionDisabled;
    expect(project(true)).toBe(true);
    expect(project(false)).toBe(false);
    expect(project(true, false)).toBe(false);
  });
});

describe("New Task Workspace selection ownership", () => {
  it("selects the ordinary default when there is no initiating Workspace", () => {
    const fallback = workspace("default", "Default", true);
    expect(
      updateWorkspaceSelection(initial(undefined), {
        type: "catalog-loaded",
        defaultWorkspace: fallback,
      }).selection,
    ).toEqual({ state: "committed", row: fallback, owner: "automatic" });
  });

  it("keeps catalog selection pending until initiating resolution completes", () => {
    const fallback = workspace("default", "Default", true);
    const pending = updateWorkspaceSelection(initial({ state: "pending" }), {
      type: "catalog-loaded",
      defaultWorkspace: fallback,
    });
    expect(pending.selection).toEqual({ state: "uncommitted" });
    expect(
      updateWorkspaceSelection(pending, {
        type: "initiating-attached",
        row: workspace("source"),
      }).selection,
    ).toEqual({ state: "committed", row: workspace("source"), owner: "automatic" });
  });

  it.each(["catalog-first", "exact-first"])(
    "selects the default only after detached initiating and catalog events arrive %s",
    (order) => {
      const fallback = workspace("default", "Default", true);
      let state = initial({ state: "pending" });
      state =
        order === "catalog-first"
          ? updateWorkspaceSelection(state, {
              type: "catalog-loaded",
              defaultWorkspace: fallback,
            })
          : updateWorkspaceSelection(state, { type: "initiating-not-attached" });
      expect(state.selection).toEqual({ state: "uncommitted" });

      state =
        order === "catalog-first"
          ? updateWorkspaceSelection(state, { type: "initiating-not-attached" })
          : updateWorkspaceSelection(state, {
              type: "catalog-loaded",
              defaultWorkspace: fallback,
            });
      expect(state.selection).toEqual({ state: "committed", row: fallback, owner: "automatic" });
    },
  );

  it("allows user selection while the initiating read is failed and preserves it on retry", () => {
    const chosen = workspace("chosen");
    let state = updateWorkspaceSelection(initial({ state: "failed", error: new Error("failed") }), {
      type: "user-selected",
      row: chosen,
    });
    state = updateWorkspaceSelection(state, { type: "initiating-retry" });
    state = updateWorkspaceSelection(state, {
      type: "initiating-attached",
      row: workspace("source"),
    });
    expect(state.selection).toEqual({ state: "committed", row: chosen, owner: "user" });
  });
});

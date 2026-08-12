import type { WorkspaceCatalogPage, WorkspaceCatalogRow } from "@/api";

export type InitiatingWorkspaceResolution =
  | Readonly<{ state: "pending" }>
  | Readonly<{ state: "attached"; row: WorkspaceCatalogRow }>
  | Readonly<{ state: "not_attached" }>
  | Readonly<{ state: "failed"; error: Error }>;

export type WorkspaceCatalogResolution =
  Readonly<{ state: "pending" }> | Readonly<{ state: "loaded"; defaultWorkspace: WorkspaceCatalogRow }>;

export type WorkspaceCommittedSelection = Readonly<{
  state: "committed";
  row: WorkspaceCatalogRow;
  owner: "automatic" | "user";
}>;

export type WorkspaceSelectionState = Readonly<{
  catalog: WorkspaceCatalogResolution;
  initiating: InitiatingWorkspaceResolution | undefined;
  selection: Readonly<{ state: "uncommitted" }> | WorkspaceCommittedSelection;
}>;

export type WorkspaceSelectionEvent =
  | Readonly<{ type: "catalog-loaded"; defaultWorkspace: WorkspaceCatalogRow }>
  | Readonly<{ type: "initiating-attached"; row: WorkspaceCatalogRow }>
  | Readonly<{ type: "initiating-not-attached" }>
  | Readonly<{ type: "initiating-failed"; error: Error }>
  | Readonly<{ type: "initiating-retry" }>
  | Readonly<{ type: "user-selected"; row: WorkspaceCatalogRow }>;

export type ProjectWorkspaceSelectorProjection = Readonly<{
  rows: readonly WorkspaceCatalogRow[];
  selectionDisabled: boolean;
}>;

export function projectWorkspaceSelectorProjection({
  catalogPages,
  initiatingRow,
  selectedSnapshot,
  catalogExhausted,
}: Readonly<{
  catalogPages: readonly WorkspaceCatalogPage[];
  initiatingRow: WorkspaceCatalogRow | undefined;
  selectedSnapshot: WorkspaceCatalogRow | undefined;
  catalogExhausted: boolean;
}>): ProjectWorkspaceSelectorProjection {
  const loadedRows = catalogPages.flatMap((catalogPage) => catalogPage.workspaces);
  const loadedIDs = new Set(loadedRows.map(({ id }) => id));
  const selectedCandidate =
    selectedSnapshot === undefined || loadedIDs.has(selectedSnapshot.id) ? undefined : selectedSnapshot;
  const candidates = [initiatingRow, selectedCandidate, ...loadedRows];
  const rows = uniqueRows(candidates);
  return {
    rows,
    selectionDisabled: catalogExhausted && rows.length === 1 && selectedSnapshot !== undefined,
  };
}

export function updateWorkspaceSelection(
  state: WorkspaceSelectionState,
  event: WorkspaceSelectionEvent,
): WorkspaceSelectionState {
  const updated = applyWorkspaceSelectionEvent(state, event);
  if (updated.selection.state === "committed" && updated.selection.owner === "user") {
    return updated;
  }
  const automaticRow = automaticWorkspaceSelection(updated);
  return automaticRow === undefined
    ? { ...updated, selection: { state: "uncommitted" } }
    : {
        ...updated,
        selection: { state: "committed", row: automaticRow, owner: "automatic" },
      };
}

function applyWorkspaceSelectionEvent(
  state: WorkspaceSelectionState,
  event: WorkspaceSelectionEvent,
): WorkspaceSelectionState {
  switch (event.type) {
    case "catalog-loaded":
      return {
        ...state,
        catalog: { state: "loaded", defaultWorkspace: event.defaultWorkspace },
      };
    case "initiating-attached":
      return {
        ...state,
        initiating: { state: "attached", row: event.row },
      };
    case "initiating-not-attached":
      return { ...state, initiating: { state: "not_attached" } };
    case "initiating-failed":
      return { ...state, initiating: { state: "failed", error: event.error } };
    case "initiating-retry":
      return { ...state, initiating: { state: "pending" } };
    case "user-selected":
      return {
        ...state,
        selection: { state: "committed", row: event.row, owner: "user" },
      };
  }
}

function automaticWorkspaceSelection(state: WorkspaceSelectionState): WorkspaceCatalogRow | undefined {
  if (state.initiating?.state === "attached") {
    return state.initiating.row;
  }
  if (
    state.catalog.state === "loaded" &&
    (state.initiating === undefined || state.initiating.state === "not_attached")
  ) {
    return state.catalog.defaultWorkspace;
  }
  return undefined;
}

function uniqueRows(
  candidates: readonly (WorkspaceCatalogRow | undefined)[],
): readonly WorkspaceCatalogRow[] {
  const rows: WorkspaceCatalogRow[] = [];
  const emitted = new Set<string>();
  for (const candidate of candidates) {
    if (candidate === undefined || emitted.has(candidate.id)) continue;
    emitted.add(candidate.id);
    rows.push(candidate);
  }
  return rows;
}

import type { TaskLabelFilter } from "@/api";

export type LabelFilterState = Readonly<{
  filter: TaskLabelFilter;
  namedMode: "any" | "all";
}>;

export type LabelFilterAction =
  | Readonly<{
      type: "named.toggle";
      labelID: string;
    }>
  | Readonly<{
      type: "named.mode";
      mode: "any" | "all";
    }>
  | Readonly<{
      type: "unlabeled.toggle";
    }>
  | Readonly<{
      type: "clear";
    }>
  | Readonly<{
      type: "label.deleted";
      labelID: string;
    }>;

export function createLabelFilterState(): LabelFilterState {
  return {
    filter: { kind: "none" },
    namedMode: "any",
  };
}

export function reduceLabelFilterState(state: LabelFilterState, action: LabelFilterAction): LabelFilterState {
  switch (action.type) {
    case "named.toggle":
      return toggleNamedLabel(state, action.labelID);
    case "named.mode":
      return setNamedMode(state, action.mode);
    case "unlabeled.toggle":
      return {
        filter: state.filter.kind === "unlabeled" ? { kind: "none" } : { kind: "unlabeled" },
        namedMode: state.namedMode,
      };
    case "clear":
      return createLabelFilterState();
    case "label.deleted":
      return pruneDeletedLabel(state, action.labelID);
  }
}

export function reconcileLabelFilterState(
  state: LabelFilterState,
  catalogLabelIDs: readonly string[],
): LabelFilterState {
  if (state.filter.kind !== "named") {
    return state;
  }
  const selectedLabelIDs = state.filter.labelIDs;
  const available = new Set(catalogLabelIDs);
  const labelIDs = selectedLabelIDs.filter((labelID) => available.has(labelID)).sort();
  if (
    labelIDs.length === selectedLabelIDs.length &&
    labelIDs.every((labelID, index) => labelID === selectedLabelIDs[index])
  ) {
    return state;
  }
  return {
    filter:
      labelIDs.length === 0
        ? { kind: "none" }
        : {
            ...state.filter,
            labelIDs,
          },
    namedMode: state.namedMode,
  };
}

function toggleNamedLabel(state: LabelFilterState, labelID: string): LabelFilterState {
  const selected = state.filter.kind === "named" ? new Set(state.filter.labelIDs) : new Set<string>();
  if (selected.has(labelID)) {
    selected.delete(labelID);
  } else {
    selected.add(labelID);
  }
  const labelIDs = [...selected].sort();
  return {
    filter:
      labelIDs.length === 0
        ? { kind: "none" }
        : {
            kind: "named",
            mode: state.namedMode,
            labelIDs,
          },
    namedMode: state.namedMode,
  };
}

function setNamedMode(state: LabelFilterState, mode: "any" | "all"): LabelFilterState {
  return {
    filter: state.filter.kind === "named" ? { ...state.filter, mode } : state.filter,
    namedMode: mode,
  };
}

function pruneDeletedLabel(state: LabelFilterState, labelID: string): LabelFilterState {
  if (state.filter.kind !== "named" || !state.filter.labelIDs.includes(labelID)) {
    return state;
  }
  const remainingLabelIDs = state.filter.labelIDs.filter((selectedLabelID) => selectedLabelID !== labelID);
  return {
    filter:
      remainingLabelIDs.length === 0 ? { kind: "none" } : { ...state.filter, labelIDs: remainingLabelIDs },
    namedMode: state.namedMode,
  };
}

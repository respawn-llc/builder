import { canonicalTaskLabelFilter, taskLabelFiltersEqual, type CanonicalTaskLabelFilter } from "@/api";

export type LabelFilterState = Readonly<{
  filter: CanonicalTaskLabelFilter;
  namedMode: "any" | "all";
}>;

export type LabelFilterAction =
  | Readonly<{
      type: "named.cycle";
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
    case "named.cycle":
      return cycleNamedLabel(state, action.labelID);
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
  const available = new Set(catalogLabelIDs);
  const nextFilter = namedFilter(
    state.filter.mode,
    state.filter.labelIDs.filter((labelID) => available.has(labelID)),
    state.filter.excludedLabelIDs.filter((labelID) => available.has(labelID)),
  );
  if (taskLabelFiltersEqual(state.filter, nextFilter)) {
    return state;
  }
  return {
    filter: nextFilter,
    namedMode: state.namedMode,
  };
}

function cycleNamedLabel(state: LabelFilterState, labelID: string): LabelFilterState {
  const labelIDs = new Set(state.filter.kind === "named" ? state.filter.labelIDs : []);
  const excludedLabelIDs = new Set(state.filter.kind === "named" ? state.filter.excludedLabelIDs : []);
  if (labelIDs.delete(labelID)) {
    excludedLabelIDs.add(labelID);
  } else if (!excludedLabelIDs.delete(labelID)) {
    labelIDs.add(labelID);
  }
  return {
    filter: namedFilter(state.namedMode, labelIDs, excludedLabelIDs),
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
  if (
    state.filter.kind !== "named" ||
    (!state.filter.labelIDs.includes(labelID) && !state.filter.excludedLabelIDs.includes(labelID))
  ) {
    return state;
  }
  return {
    filter: namedFilter(
      state.filter.mode,
      state.filter.labelIDs.filter((selectedLabelID) => selectedLabelID !== labelID),
      state.filter.excludedLabelIDs.filter((selectedLabelID) => selectedLabelID !== labelID),
    ),
    namedMode: state.namedMode,
  };
}

function namedFilter(
  mode: "any" | "all",
  labelIDs: Iterable<string>,
  excludedLabelIDs: Iterable<string>,
): CanonicalTaskLabelFilter {
  const canonical = canonicalTaskLabelFilter({
    kind: "named",
    mode,
    labelIDs: [...new Set(labelIDs)],
    excludedLabelIDs: [...new Set(excludedLabelIDs)],
  });
  if (canonical.kind !== "named") {
    throw new Error("Named label filter canonicalization returned a non-named filter.");
  }
  if (canonical.labelIDs.length === 0 && canonical.excludedLabelIDs.length === 0) {
    return { kind: "none" };
  }
  return canonical;
}

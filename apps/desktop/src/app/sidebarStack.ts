import {
  sidebarDestinationMatchesTask,
  type SidebarDestination,
  type SidebarDestinationSnapshot,
} from "@/app-facade";

export const sidebarStackCapacity = 50;

export type SidebarStackEntry = Readonly<{
  entryID: string;
  destination: SidebarDestination;
  snapshot?: SidebarDestinationSnapshot | undefined;
}>;

export type SidebarStackState = Readonly<{
  activationID: string;
  lifecycleID: string;
  entries: readonly SidebarStackEntry[];
}>;

export type SidebarStackAction =
  | Readonly<{
      type: "open";
      activationID?: string;
      lifecycleID: string;
      entryID: string;
      destination: SidebarDestination;
      snapshot?: SidebarDestinationSnapshot | undefined;
    }>
  | Readonly<{
      type: "replace";
      activationID: string;
      entryID: string;
      destination: SidebarDestination;
    }>
  | Readonly<{
      type: "push";
      activationID: string;
      entryID: string;
      lifecycleID: string;
      sourceEntryID: string;
      destination: SidebarDestination;
    }>
  | Readonly<{ type: "back"; activationID: string; lifecycleID: string; entryID: string }>
  | Readonly<{
      type: "remove";
      activationID?: string;
      lifecycleID: string;
      entryID: string;
    }>
  | Readonly<{
      type: "capture";
      lifecycleID: string;
      entryID: string;
      snapshot: SidebarDestinationSnapshot;
    }>
  | Readonly<{ type: "close"; lifecycleID: string }>;

export function createSidebarStack(
  lifecycleID: string,
  root: SidebarStackEntry,
  activationID = lifecycleID,
): SidebarStackState {
  return {
    activationID,
    lifecycleID,
    entries: [root],
  };
}

export function sidebarStackReducer(
  state: SidebarStackState | null,
  action: SidebarStackAction,
): SidebarStackState | null {
  switch (action.type) {
    case "open":
      return reduceOpen(action);
    case "replace":
      return reduceReplace(state, action);
    case "push":
      return reducePush(state, action);
    case "back":
      return reduceBack(state, action);
    case "remove":
      return reduceRemove(state, action);
    case "capture":
      return reduceCapture(state, action);
    case "close":
      return state?.lifecycleID === action.lifecycleID ? null : state;
  }
}

function reduceOpen(action: Extract<SidebarStackAction, { type: "open" }>): SidebarStackState {
  return createSidebarStack(
    action.lifecycleID,
    {
      entryID: action.entryID,
      destination: action.destination,
      ...(action.snapshot === undefined ? {} : { snapshot: action.snapshot }),
    },
    action.activationID,
  );
}

function reduceReplace(
  state: SidebarStackState | null,
  action: Extract<SidebarStackAction, { type: "replace" }>,
): SidebarStackState | null {
  if (state === null || state.entries.length === 0) {
    return state;
  }
  const existingTaskIndex = findTaskDetailIndex(state.entries, action.destination);
  if (existingTaskIndex !== undefined) {
    const retainedEntry = state.entries[existingTaskIndex];
    if (retainedEntry === undefined) {
      return state;
    }
    const isCurrentEntry = existingTaskIndex === state.entries.length - 1;
    return {
      ...state,
      activationID: action.activationID,
      entries: [
        ...state.entries.slice(0, isCurrentEntry ? -1 : existingTaskIndex),
        {
          ...retainedEntry,
          ...(isCurrentEntry ? { entryID: action.entryID } : {}),
          destination: action.destination,
        },
      ],
    };
  }
  return {
    ...state,
    activationID: action.activationID,
    entries: [...state.entries.slice(0, -1), { entryID: action.entryID, destination: action.destination }],
  };
}

function reducePush(
  state: SidebarStackState | null,
  action: Extract<SidebarStackAction, { type: "push" }>,
): SidebarStackState | null {
  if (state === null) return state;
  if (action.lifecycleID !== state.lifecycleID || state.entries.at(-1)?.entryID !== action.sourceEntryID) {
    return state;
  }
  const existingTaskIndex = findTaskDetailIndex(state.entries, action.destination);
  if (existingTaskIndex !== undefined) {
    return {
      ...state,
      activationID: action.activationID,
      entries: state.entries.slice(0, existingTaskIndex + 1),
    };
  }
  const root = state.entries[0];
  if (root === undefined) return state;
  const entries =
    state.entries.length >= sidebarStackCapacity ? [root, ...state.entries.slice(2)] : state.entries;
  return {
    ...state,
    activationID: action.activationID,
    entries: [
      ...entries.map((entry, index) => (index === entries.length - 1 ? deactivateEntry(entry) : entry)),
      { entryID: action.entryID, destination: action.destination },
    ],
  };
}

function reduceBack(
  state: SidebarStackState | null,
  action: Extract<SidebarStackAction, { type: "back" }>,
): SidebarStackState | null {
  return state?.lifecycleID !== action.lifecycleID ||
    state.entries.length <= 1 ||
    state.entries.at(-1)?.entryID !== action.entryID
    ? state
    : {
        ...state,
        activationID: action.activationID,
        entries: state.entries.slice(0, -1),
      };
}

function reduceRemove(
  state: SidebarStackState | null,
  action: Extract<SidebarStackAction, { type: "remove" }>,
): SidebarStackState | null {
  if (state?.lifecycleID !== action.lifecycleID) return state;
  const index = sidebarEntryIndex(state.entries, action.entryID);
  return index === undefined
    ? state
    : state.entries.length === 1
      ? null
      : {
          ...state,
          activationID:
            index === state.entries.length - 1
              ? (action.activationID ?? state.activationID)
              : state.activationID,
          entries: state.entries.filter((_, i) => i !== index),
        };
}

function reduceCapture(
  state: SidebarStackState | null,
  action: Extract<SidebarStackAction, { type: "capture" }>,
): SidebarStackState | null {
  if (state?.lifecycleID !== action.lifecycleID) return state;
  const index = sidebarEntryIndex(state.entries, action.entryID);
  return index === undefined
    ? state
    : {
        ...state,
        entries: state.entries.map((entry, i) =>
          i === index ? { ...entry, snapshot: action.snapshot } : entry,
        ),
      };
}

function sidebarEntryIndex(entries: readonly SidebarStackEntry[], entryID: string): number | undefined {
  for (const [index, entry] of entries.entries()) {
    if (entry.entryID === entryID) {
      return index;
    }
  }
  return undefined;
}

function deactivateEntry(entry: SidebarStackEntry): SidebarStackEntry {
  if (entry.destination.kind !== "taskDetail") {
    return entry;
  }
  const { initialFocus, ...destination } = entry.destination;
  if (initialFocus === undefined) return entry;
  return { ...entry, destination };
}

export function findTaskDetailIndex(
  entries: readonly SidebarStackEntry[],
  destination: SidebarDestination,
): number | undefined {
  if (destination.kind !== "taskDetail") {
    return undefined;
  }
  for (const [index, entry] of entries.entries()) {
    if (sidebarDestinationMatchesTask(entry.destination, destination.taskID)) {
      return index;
    }
  }
  return undefined;
}

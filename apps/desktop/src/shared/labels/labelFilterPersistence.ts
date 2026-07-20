import { z } from "zod";

import {
  readBrowserStorage,
  writeBrowserStorage,
  type AppStorageNamespace,
  type BrowserStorageError,
  type BrowserStorageResult,
} from "@/app-facade";
import { createLabelFilterState, reconcileLabelFilterState, type LabelFilterState } from "./labelFilterState";

const labelFilterStoragePrefix = "desktop.projectLabelFilter.v1";
const labelFilterModeSchema = z.enum(["any", "all"]);
const storedLabelIDsSchema = z
  .array(z.uuidv4())
  .max(100)
  .refine((labelIDs) => new Set(labelIDs).size === labelIDs.length);
const storedLabelFilterSchema = z.discriminatedUnion("kind", [
  z
    .object({
      version: z.literal(1),
      kind: z.literal("none"),
      mode: labelFilterModeSchema,
    })
    .strict(),
  z
    .object({
      version: z.literal(1),
      kind: z.literal("named"),
      mode: labelFilterModeSchema,
      labelIDs: storedLabelIDsSchema,
    })
    .strict(),
  z
    .object({
      version: z.literal(1),
      kind: z.literal("unlabeled"),
      mode: labelFilterModeSchema,
    })
    .strict(),
]);

export type LabelFilterPersistenceReadResult =
  | Readonly<{
      ok: true;
      state: LabelFilterState;
    }>
  | Readonly<{
      ok: false;
      state: LabelFilterState;
      error: BrowserStorageError;
    }>;

export function readPersistedLabelFilterState(
  namespace: AppStorageNamespace,
  projectID: string,
  catalogLabelIDs: readonly string[],
): LabelFilterPersistenceReadResult {
  const key = labelFilterStorageKey(namespace, projectID);
  const stored = readBrowserStorage("local", key);
  if (!stored.ok) {
    return {
      ok: false,
      state: createLabelFilterState(),
      error: stored.error,
    };
  }
  if (stored.value === null) {
    return {
      ok: true,
      state: createLabelFilterState(),
    };
  }
  const parsed = parseStoredLabelFilter(stored.value);
  if (parsed === null) {
    return {
      ok: true,
      state: createLabelFilterState(),
    };
  }
  const unprunedState = restoreLabelFilterState(parsed);
  const state = reconcileLabelFilterState(unprunedState, catalogLabelIDs);
  if (state !== unprunedState) {
    const written = writePersistedLabelFilterState(namespace, projectID, state);
    if (!written.ok) {
      return {
        ok: false,
        state,
        error: written.error,
      };
    }
  }
  return {
    ok: true,
    state,
  };
}

export function writePersistedLabelFilterState(
  namespace: AppStorageNamespace,
  projectID: string,
  state: LabelFilterState,
): BrowserStorageResult<void> {
  return writeBrowserStorage(
    "local",
    labelFilterStorageKey(namespace, projectID),
    JSON.stringify(serializeLabelFilterState(state)),
  );
}

function labelFilterStorageKey(namespace: AppStorageNamespace, projectID: string): string {
  return `${labelFilterStoragePrefix}:${JSON.stringify([namespace.kind, namespace.identity, projectID])}`;
}

function parseStoredLabelFilter(raw: string): z.output<typeof storedLabelFilterSchema> | null {
  try {
    const parsed: unknown = JSON.parse(raw);
    const result = storedLabelFilterSchema.safeParse(parsed);
    return result.success ? result.data : null;
  } catch {
    return null;
  }
}

function restoreLabelFilterState(stored: z.output<typeof storedLabelFilterSchema>): LabelFilterState {
  switch (stored.kind) {
    case "none":
      return {
        filter: { kind: "none" },
        namedMode: stored.mode,
      };
    case "unlabeled":
      return {
        filter: { kind: "unlabeled" },
        namedMode: stored.mode,
      };
    case "named": {
      return {
        filter: {
          kind: "named",
          mode: stored.mode,
          labelIDs: [...stored.labelIDs].sort(),
        },
        namedMode: stored.mode,
      };
    }
  }
}

function serializeLabelFilterState(state: LabelFilterState): z.input<typeof storedLabelFilterSchema> {
  switch (state.filter.kind) {
    case "none":
    case "unlabeled":
      return {
        version: 1,
        kind: state.filter.kind,
        mode: state.namedMode,
      };
    case "named":
      return {
        version: 1,
        kind: "named",
        mode: state.namedMode,
        labelIDs: [...state.filter.labelIDs].sort(),
      };
  }
}

import { useCallback, useEffect, useReducer, useRef } from "react";

import type { BrowserStorageError } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { readPersistedLabelFilterState, writePersistedLabelFilterState } from "./labelFilterPersistence";
import {
  createLabelFilterState,
  reconcileLabelFilterState,
  reduceLabelFilterState,
  type LabelFilterAction,
  type LabelFilterState,
} from "./labelFilterState";

export class LabelFilterStorageNamespaceError extends Error {
  constructor() {
    super("Label filter storage is unavailable because startup did not resolve a storage namespace.");
    this.name = "LabelFilterStorageNamespaceError";
  }
}

export type LabelFilterPersistenceStatus =
  | Readonly<{ status: "loading" }>
  | Readonly<{ status: "ready" }>
  | Readonly<{
      status: "error";
      error: BrowserStorageError | LabelFilterStorageNamespaceError;
    }>;

export type ProjectLabelFilterController = Readonly<{
  state: LabelFilterState;
  persistence: LabelFilterPersistenceStatus;
  dispatch(action: LabelFilterAction): void;
}>;

type ManagedFilterState = Readonly<{
  filter: LabelFilterState;
  persistence: LabelFilterPersistenceStatus;
}>;

type ManagedFilterAction =
  | Readonly<{
      type: "restore";
      state: LabelFilterState;
      persistence: LabelFilterPersistenceStatus;
    }>
  | Readonly<{
      type: "catalog.reconcile";
      labelIDs: readonly string[];
    }>
  | Readonly<{
      type: "user";
      action: LabelFilterAction;
    }>
  | Readonly<{
      type: "storage.failed";
      error: BrowserStorageError;
    }>;

export function useManagedProjectLabelFilter(
  projectID: string,
  catalogLabelIDs: readonly string[] | null,
): ProjectLabelFilterController {
  const { storageNamespace } = useAppServices();
  const [managed, dispatchManaged] = useReducer(manageFilterState, undefined, createManagedFilterState);
  const hydratedSource = useRef<HydratedFilterSource | null>(null);
  const skipPersistence = useRef(false);

  useEffect(() => {
    if (catalogLabelIDs === null) {
      return;
    }
    const source = filterSource(projectID, storageNamespace);
    if (!filterSourcesEqual(hydratedSource.current, source)) {
      hydratedSource.current = source;
      skipPersistence.current = true;
      if (storageNamespace === null) {
        dispatchManaged({
          type: "restore",
          state: createLabelFilterState(),
          persistence: {
            status: "error",
            error: new LabelFilterStorageNamespaceError(),
          },
        });
        return;
      }
      const result = readPersistedLabelFilterState(storageNamespace, projectID, catalogLabelIDs);
      dispatchManaged({
        type: "restore",
        state: result.state,
        persistence: result.ok ? { status: "ready" } : { status: "error", error: result.error },
      });
      return;
    }
    dispatchManaged({ type: "catalog.reconcile", labelIDs: catalogLabelIDs });
  }, [catalogLabelIDs, projectID, storageNamespace]);

  useEffect(() => {
    if (skipPersistence.current) {
      skipPersistence.current = false;
      return;
    }
    if (managed.persistence.status !== "ready" || storageNamespace === null) {
      return;
    }
    const result = writePersistedLabelFilterState(storageNamespace, projectID, managed.filter);
    if (!result.ok) {
      dispatchManaged({ type: "storage.failed", error: result.error });
    }
  }, [managed.filter, managed.persistence.status, projectID, storageNamespace]);

  const dispatch = useCallback((action: LabelFilterAction) => {
    dispatchManaged({ type: "user", action });
  }, []);

  return {
    state: managed.filter,
    persistence: managed.persistence,
    dispatch,
  };
}

function manageFilterState(state: ManagedFilterState, action: ManagedFilterAction): ManagedFilterState {
  switch (action.type) {
    case "restore":
      return {
        filter: action.state,
        persistence: action.persistence,
      };
    case "catalog.reconcile": {
      const filter = reconcileLabelFilterState(state.filter, action.labelIDs);
      return filter === state.filter
        ? state
        : {
            filter,
            persistence: retryablePersistence(state.persistence),
          };
    }
    case "user": {
      const filter = reduceLabelFilterState(state.filter, action.action);
      return {
        filter,
        persistence: retryablePersistence(state.persistence),
      };
    }
    case "storage.failed":
      return {
        ...state,
        persistence: { status: "error", error: action.error },
      };
  }
}

function createManagedFilterState(): ManagedFilterState {
  return {
    filter: createLabelFilterState(),
    persistence: { status: "loading" },
  };
}

function retryablePersistence(persistence: LabelFilterPersistenceStatus): LabelFilterPersistenceStatus {
  if (persistence.status === "error" && persistence.error instanceof LabelFilterStorageNamespaceError) {
    return persistence;
  }
  return { status: "ready" };
}

type HydratedFilterSource = Readonly<{
  projectID: string;
  namespace:
    | Readonly<{ kind: "unavailable" }>
    | Readonly<{
        kind: "available";
        namespaceKind: "native-persistence-root" | "browser-endpoint";
        identity: string;
      }>;
}>;

function filterSource(
  projectID: string,
  namespace: ReturnType<typeof useAppServices>["storageNamespace"],
): HydratedFilterSource {
  if (namespace === null) {
    return {
      projectID,
      namespace: { kind: "unavailable" },
    };
  }
  return {
    projectID,
    namespace: {
      kind: "available",
      namespaceKind: namespace.kind,
      identity: namespace.identity,
    },
  };
}

function filterSourcesEqual(left: HydratedFilterSource | null, right: HydratedFilterSource): boolean {
  if (left === null) {
    return false;
  }
  if (left.projectID !== right.projectID || left.namespace.kind !== right.namespace.kind) {
    return false;
  }
  if (left.namespace.kind === "unavailable" || right.namespace.kind === "unavailable") {
    return true;
  }
  return (
    left.namespace.namespaceKind === right.namespace.namespaceKind &&
    left.namespace.identity === right.namespace.identity
  );
}

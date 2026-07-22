import type { QueryClient } from "@tanstack/react-query";

import type { ProjectLabelCatalog } from "@/api";
import type { LabelFilterAction } from "./labelFilterState";
import { createProjectCatalogAuthority, type ProjectCatalogAuthority } from "./projectCatalogAuthority";

export type ProjectCatalogAuthorityLease = Readonly<{
  authority: ProjectCatalogAuthority;
  dispatchFilterAction(action: LabelFilterAction): void;
  retain(onFilterAction: (action: LabelFilterAction) => void): () => void;
}>;

interface RegistryEntry {
  authority: ProjectCatalogAuthority;
  cleanup: ReturnType<typeof setTimeout> | null;
  filterListeners: Map<(action: LabelFilterAction) => void, number>;
  references: number;
}

const registries = new WeakMap<QueryClient, ProjectCatalogAuthorityRegistry>();

export function projectCatalogAuthorityRegistryFor(
  queryClient: QueryClient,
): ProjectCatalogAuthorityRegistry {
  const existing = registries.get(queryClient);
  if (existing !== undefined) {
    return existing;
  }
  const registry = new ProjectCatalogAuthorityRegistry(queryClient);
  registries.set(queryClient, registry);
  return registry;
}

class ProjectCatalogAuthorityRegistry {
  readonly #queryClient: QueryClient;
  readonly #entries = new Map<string, RegistryEntry>();

  constructor(queryClient: QueryClient) {
    this.#queryClient = queryClient;
  }

  prepare(projectID: string, listCatalog: () => Promise<ProjectLabelCatalog>): ProjectCatalogAuthorityLease {
    const entry = this.#entries.get(projectID) ?? this.#createEntry(projectID, listCatalog);
    return {
      authority: entry.authority,
      dispatchFilterAction: (action) => {
        for (const listener of entry.filterListeners.keys()) {
          listener(action);
        }
      },
      retain: (onFilterAction) => this.#retain(projectID, entry, onFilterAction),
    };
  }

  #createEntry(projectID: string, listCatalog: () => Promise<ProjectLabelCatalog>): RegistryEntry {
    const entry: RegistryEntry = {
      authority: createProjectCatalogAuthority({
        projectID,
        queryClient: this.#queryClient,
        listCatalog,
      }),
      cleanup: null,
      filterListeners: new Map(),
      references: 0,
    };
    this.#entries.set(projectID, entry);
    entry.cleanup = setTimeout(() => {
      entry.cleanup = null;
      if (entry.references === 0 && this.#entries.get(projectID) === entry) {
        this.#entries.delete(projectID);
      }
    }, 0);
    return entry;
  }

  #retain(
    projectID: string,
    entry: RegistryEntry,
    onFilterAction: (action: LabelFilterAction) => void,
  ): () => void {
    if (entry.cleanup !== null) {
      clearTimeout(entry.cleanup);
      entry.cleanup = null;
    }
    entry.references += 1;
    entry.filterListeners.set(onFilterAction, (entry.filterListeners.get(onFilterAction) ?? 0) + 1);
    let released = false;
    return () => {
      if (released) {
        return;
      }
      released = true;
      entry.cleanup = setTimeout(() => {
        entry.cleanup = null;
        entry.references -= 1;
        const listenerReferences = entry.filterListeners.get(onFilterAction);
        if (listenerReferences === undefined || listenerReferences === 1) {
          entry.filterListeners.delete(onFilterAction);
        } else {
          entry.filterListeners.set(onFilterAction, listenerReferences - 1);
        }
        if (entry.references === 0 && this.#entries.get(projectID) === entry) {
          this.#entries.delete(projectID);
        }
      }, 0);
    };
  }
}

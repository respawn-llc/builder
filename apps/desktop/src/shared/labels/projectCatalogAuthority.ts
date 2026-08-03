import { CancelledError, type QueryClient } from "@tanstack/react-query";

import { workflowLabelMaxIDs, type ProjectLabel, type ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";

export type ProjectCatalogAuthority = Readonly<{
  read(signal: AbortSignal): Promise<ProjectLabelCatalog>;
  supersedeReads(): number;
  applyCreate(label: ProjectLabel): void;
  applyRename(label: ProjectLabel): void;
  applyDelete(labelID: string): void;
  installCatalog(catalog: ProjectLabelCatalog, generation: number): void;
  requestRefresh(): void;
}>;

export function createProjectCatalogAuthority({
  listCatalog,
  projectID,
  queryClient,
}: Readonly<{
  listCatalog: () => Promise<ProjectLabelCatalog>;
  projectID: string;
  queryClient: QueryClient;
}>): ProjectCatalogAuthority {
  return new ProjectCatalogAuthorityImpl(projectID, queryClient, listCatalog);
}

type ActiveCatalogRead = Readonly<{
  generation: number;
  promise: Promise<ProjectLabelCatalog>;
}>;

class ProjectCatalogAuthorityImpl implements ProjectCatalogAuthority {
  readonly #projectID: string;
  readonly #queryClient: QueryClient;
  readonly #listCatalog: () => Promise<ProjectLabelCatalog>;
  readonly #queryKey: ReturnType<typeof queryKeys.projectLabels>;
  readonly #deletedLabelIDs = new Set<string>();
  #generation = 0;
  #activeRead: ActiveCatalogRead | null = null;
  #refreshNeeded = false;

  constructor(projectID: string, queryClient: QueryClient, listCatalog: () => Promise<ProjectLabelCatalog>) {
    this.#projectID = projectID;
    this.#queryClient = queryClient;
    this.#listCatalog = listCatalog;
    this.#queryKey = queryKeys.projectLabels(projectID);
  }

  async read(signal: AbortSignal): Promise<ProjectLabelCatalog> {
    const generation = this.#generation;
    const activeRead = this.#activeRead ?? this.#startRead(generation);
    const catalog = await activeRead.promise;
    if (signal.aborted || generation !== this.#generation || activeRead.generation !== generation) {
      throw new CancelledError({ revert: true, silent: true });
    }
    this.#deletedLabelIDs.clear();
    return catalog;
  }

  applyCreate(label: ProjectLabel): void {
    this.#advance(true);
    this.#patchLabel(label, true);
    this.#startRefreshIfIdle();
  }

  applyRename(label: ProjectLabel): void {
    this.#advance(true);
    this.#patchLabel(label, false);
    this.#startRefreshIfIdle();
  }

  applyDelete(labelID: string): void {
    this.#advance(true);
    this.#deletedLabelIDs.add(labelID);
    if (this.#deletedLabelIDs.size > workflowLabelMaxIDs) {
      throw new Error(
        `Project catalog authority tombstones exceeded the ${String(workflowLabelMaxIDs)}-label bound for Project ${this.#projectID}.`,
      );
    }
    this.#queryClient.setQueryData<ProjectLabelCatalog>(this.#queryKey, (catalog) => {
      if (catalog === undefined) {
        return undefined;
      }
      return {
        ...catalog,
        labels: catalog.labels.filter((label) => label.id !== labelID),
      };
    });
    this.#startRefreshIfIdle();
  }

  requestRefresh(): void {
    this.#advance(true);
    this.#startRefreshIfIdle();
  }

  supersedeReads(): number {
    return this.#advance(false);
  }

  installCatalog(catalog: ProjectLabelCatalog, generation: number): void {
    this.#assertProject(catalog);
    if (generation !== this.#generation) {
      this.#startRefreshIfIdle();
      return;
    }
    this.#advance(false);
    this.#deletedLabelIDs.clear();
    this.#queryClient.setQueryData(this.#queryKey, catalog);
  }

  #advance(refreshNeeded: boolean): number {
    this.#generation += 1; this.#refreshNeeded ||= refreshNeeded;
    void this.#queryClient
      .cancelQueries(
        {
          queryKey: this.#queryKey,
          exact: true,
        },
        {
          revert: false,
          silent: true,
        },
      )
      .catch(() => undefined);
    return this.#generation;
  }

  #patchLabel(label: ProjectLabel, prepend: boolean): void {
    this.#deletedLabelIDs.delete(label.id);
    this.#queryClient.setQueryData<ProjectLabelCatalog>(this.#queryKey, (catalog) => {
      if (catalog === undefined) {
        return undefined;
      }
      const index = catalog.labels.findIndex((candidate) => candidate.id === label.id);
      if (!prepend && index < 0) {
        return catalog;
      }
      const labels =
        index < 0
          ? [label, ...catalog.labels]
          : catalog.labels.map((candidate, candidateIndex) => (candidateIndex === index ? label : candidate));
      return {
        ...catalog,
        labels,
      };
    });
  }

  #assertProject(catalog: ProjectLabelCatalog): void {
    if (catalog.projectID !== this.#projectID) {
      throw new Error(
        `Project catalog authority received ${catalog.projectID} while serving ${this.#projectID}.`,
      );
    }
  }

  #startRead(generation: number): ActiveCatalogRead {
    const activeRead = {
      generation,
      promise: this.#listCatalog(),
    };
    this.#activeRead = activeRead;
    void activeRead.promise.then(
      () => {
        this.#settleRead(activeRead);
      },
      () => {
        this.#settleRead(activeRead);
      },
    );
    return activeRead;
  }

  #settleRead(activeRead: ActiveCatalogRead): void {
    if (this.#activeRead !== activeRead) {
      return;
    }
    this.#activeRead = null;
    this.#startRefreshIfIdle();
  }

  #startRefreshIfIdle(): void {
    if (this.#activeRead !== null) {
      return;
    }
    if (!this.#refreshNeeded) {
      return;
    }
    this.#refreshNeeded = false;
    const refresh = this.#queryClient
      .fetchQuery({
        queryKey: this.#queryKey,
        queryFn: async ({ signal }) => this.read(signal),
        staleTime: 0,
      })
      .then(() => undefined);
    void refresh.catch(() => undefined);
  }
}

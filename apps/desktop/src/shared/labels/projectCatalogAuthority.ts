import { CancelledError, type QueryClient } from "@tanstack/react-query";

import { workflowLabelMaxIDs, type ProjectLabel, type ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";

export type ProjectCatalogAuthority = Readonly<{
  read(signal: AbortSignal): Promise<ProjectLabelCatalog>;
  applyCreate(label: ProjectLabel): void;
  applyRename(label: ProjectLabel): void;
  applyDelete(labelID: string): void;
  reorder(labelIDs: readonly string[]): void;
  requestRefresh(): void;
}>;

export function createProjectCatalogAuthority({
  listCatalog,
  reorderCatalog,
  onReorderFailure,
  projectID,
  queryClient,
}: Readonly<{
  listCatalog: () => Promise<ProjectLabelCatalog>;
  reorderCatalog: (labelIDs: readonly string[]) => Promise<ProjectLabelCatalog>;
  onReorderFailure: (error: unknown) => void;
  projectID: string;
  queryClient: QueryClient;
}>): ProjectCatalogAuthority {
  return new ProjectCatalogAuthorityImpl({
    projectID,
    queryClient,
    listCatalog,
    reorderCatalog,
    onReorderFailure,
  });
}

export function selectOrderedProjectLabels(
  catalog: readonly ProjectLabel[] | undefined,
  assignedLabelIDs: readonly string[],
): readonly ProjectLabel[] {
  if (catalog === undefined || assignedLabelIDs.length === 0) {
    return [];
  }
  const assigned = new Set(assignedLabelIDs);
  return catalog.filter((label) => assigned.has(label.id));
}

type ActiveCatalogRead = Readonly<{
  generation: number;
  promise: Promise<ProjectLabelCatalog>;
}>;

type ReorderIntent = Readonly<{
  id: number;
  requestedIDs: readonly string[];
  rollbackIDs: readonly string[];
}>;

class ProjectCatalogAuthorityImpl implements ProjectCatalogAuthority {
  readonly #projectID: string;
  readonly #queryClient: QueryClient;
  readonly #listCatalog: () => Promise<ProjectLabelCatalog>;
  readonly #reorderCatalog: (labelIDs: readonly string[]) => Promise<ProjectLabelCatalog>;
  readonly #onReorderFailure: (error: unknown) => void;
  readonly #queryKey: ReturnType<typeof queryKeys.projectLabels>;
  readonly #deletedLabelIDs = new Set<string>();
  #catalogGeneration = 0;
  #activeRead: ActiveCatalogRead | null = null;
  #refreshNeeded = false;
  #nextOrderIntentID = 0;
  #latestOrderIntent: ReorderIntent | null = null;
  #pendingReorder: ReorderIntent | null = null;
  #inFlightReorder: ReorderIntent | null = null;
  #reconcileAfterReordersDrain = false;

  constructor({
    projectID,
    queryClient,
    listCatalog,
    reorderCatalog,
    onReorderFailure,
  }: Readonly<{
    projectID: string;
    queryClient: QueryClient;
    listCatalog: () => Promise<ProjectLabelCatalog>;
    reorderCatalog: (labelIDs: readonly string[]) => Promise<ProjectLabelCatalog>;
    onReorderFailure: (error: unknown) => void;
  }>) {
    this.#projectID = projectID;
    this.#queryClient = queryClient;
    this.#listCatalog = listCatalog;
    this.#reorderCatalog = reorderCatalog;
    this.#onReorderFailure = onReorderFailure;
    this.#queryKey = queryKeys.projectLabels(projectID);
  }

  async read(signal: AbortSignal): Promise<ProjectLabelCatalog> {
    const generation = this.#catalogGeneration;
    const activeRead = this.#activeRead ?? this.#startRead(generation);
    const catalog = await activeRead.promise;
    if (signal.aborted || generation !== this.#catalogGeneration || activeRead.generation !== generation) {
      throw new CancelledError({ revert: true, silent: true });
    }
    return this.#acceptAuthoritativeCatalog(catalog);
  }

  applyCreate(label: ProjectLabel): void {
    const current = this.#currentCatalog();
    this.#advanceCatalog();
    this.#deletedLabelIDs.delete(label.id);
    const alreadyPresent = current?.labels.some((candidate) => candidate.id === label.id) === true;
    if (!alreadyPresent) {
      this.#supersedePendingForMembershipChange();
    }
    this.#patchCatalog((labels) =>
      labels.some((candidate) => candidate.id === label.id)
        ? labels.map((candidate) => (candidate.id === label.id ? label : candidate))
        : [...labels, label],
    );
    this.#startRefreshIfIdle();
  }

  applyRename(label: ProjectLabel): void {
    this.#advanceCatalog();
    this.#deletedLabelIDs.delete(label.id);
    this.#patchCatalog((labels) =>
      labels.map((candidate) => (candidate.id === label.id ? label : candidate)),
    );
    this.#startRefreshIfIdle();
  }

  applyDelete(labelID: string): void {
    const current = this.#currentCatalog();
    if (current !== undefined && !current.labels.some((label) => label.id === labelID)) {
      return;
    }
    this.#advanceCatalog();
    this.#deletedLabelIDs.add(labelID);
    if (this.#deletedLabelIDs.size > workflowLabelMaxIDs) {
      throw new Error(
        `Project catalog authority tombstones exceeded the ${String(workflowLabelMaxIDs)}-label bound for Project ${this.#projectID}.`,
      );
    }
    this.#supersedePendingForMembershipChange();
    this.#patchCatalog((labels) => labels.filter((label) => label.id !== labelID));
    this.#startRefreshIfIdle();
  }

  reorder(labelIDs: readonly string[]): void {
    const catalog = this.#currentCatalog();
    if (catalog === undefined) {
      throw new Error(`Cannot reorder Project ${this.#projectID} labels before its catalog is loaded.`);
    }
    assertExactCatalogPermutation(catalog.labels, labelIDs, this.#projectID);
    const intent: ReorderIntent = {
      id: ++this.#nextOrderIntentID,
      requestedIDs: [...labelIDs],
      rollbackIDs: catalog.labels.map((label) => label.id),
    };
    this.#advanceCatalog();
    this.#latestOrderIntent = intent;
    this.#pendingReorder = intent;
    this.#writeLabels(projectCatalogLabelsInOrder(catalog.labels, intent.requestedIDs, this.#projectID));
    this.#startNextReorder();
  }

  requestRefresh(): void {
    this.#advanceCatalog();
    this.#startRefreshIfIdle();
  }

  #advanceCatalog(): void {
    this.#catalogGeneration += 1;
    this.#refreshNeeded = true;
    void this.#queryClient.cancelQueries(
      { queryKey: this.#queryKey, exact: true },
      { revert: false, silent: true },
    );
  }

  #startRead(generation: number): ActiveCatalogRead {
    const activeRead = { generation, promise: this.#listCatalog() };
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
    this.#drain();
  }

  #acceptAuthoritativeCatalog(catalog: ProjectLabelCatalog): ProjectLabelCatalog {
    if (catalog.projectID !== this.#projectID) {
      throw new Error(
        `Project catalog response belongs to ${catalog.projectID}, expected ${this.#projectID}.`,
      );
    }
    const intent = this.#latestOrderIntent;
    if (intent !== null && !sameLabelMembership(catalog.labels, intent.requestedIDs)) {
      this.#supersedePendingForMembershipChange();
      this.#reconcileAfterReordersDrain ||= this.#inFlightReorder !== null;
    }
    this.#catalogGeneration += 1;
    this.#deletedLabelIDs.clear();
    const accepted = {
      ...catalog,
      labels: this.#projectLabelsThroughIntent(catalog.labels),
    };
    this.#queryClient.setQueryData(this.#queryKey, accepted);
    return accepted;
  }

  #currentCatalog(): ProjectLabelCatalog | undefined {
    return this.#queryClient.getQueryData<ProjectLabelCatalog>(this.#queryKey);
  }

  #patchCatalog(transform: (labels: readonly ProjectLabel[]) => readonly ProjectLabel[]): void {
    this.#queryClient.setQueryData<ProjectLabelCatalog>(this.#queryKey, (catalog) => {
      if (catalog === undefined) {
        return undefined;
      }
      return {
        ...catalog,
        labels: this.#projectLabelsThroughIntent(transform(catalog.labels)),
      };
    });
  }

  #writeLabels(labels: readonly ProjectLabel[]): void {
    this.#queryClient.setQueryData<ProjectLabelCatalog>(this.#queryKey, (catalog) =>
      catalog === undefined ? undefined : { ...catalog, labels },
    );
  }

  #projectLabelsThroughIntent(labels: readonly ProjectLabel[]): readonly ProjectLabel[] {
    const intent = this.#latestOrderIntent;
    return intent === null
      ? labels
      : projectCatalogLabelsInOrder(labels, intent.requestedIDs, this.#projectID);
  }

  #supersedePendingForMembershipChange(): void {
    this.#pendingReorder = null;
    this.#latestOrderIntent = null;
  }

  #startNextReorder(): void {
    if (this.#inFlightReorder !== null || this.#pendingReorder === null) {
      return;
    }
    const intent = this.#pendingReorder;
    this.#pendingReorder = null;
    this.#inFlightReorder = intent;
    const transportGeneration = this.#catalogGeneration;
    void this.#reorderCatalog(intent.requestedIDs).then(
      (catalog) => {
        this.#settleReorder(intent, transportGeneration, catalog, null);
      },
      (failure: unknown) => {
        this.#settleReorder(intent, transportGeneration, null, failure);
      },
    );
  }

  #settleReorder(
    intent: ReorderIntent,
    transportGeneration: number,
    catalog: ProjectLabelCatalog | null,
    failure: unknown,
  ): void {
    if (this.#inFlightReorder !== intent) {
      throw new Error(`Project ${this.#projectID} settled an unknown Label reorder intent.`);
    }
    this.#inFlightReorder = null;
    const ownsCurrentState =
      transportGeneration === this.#catalogGeneration && this.#latestOrderIntent?.id === intent.id;
    this.#applyReorderSettlement(intent, ownsCurrentState, catalog, failure);
    this.#drain();
  }

  #applyReorderSettlement(
    intent: ReorderIntent,
    ownsCurrentState: boolean,
    catalog: ProjectLabelCatalog | null,
    failure: unknown,
  ): void {
    if (failure === null && catalog !== null) {
      this.#applySuccessfulReorderSettlement(intent, ownsCurrentState, catalog);
      return;
    }
    if (failure !== null) {
      this.#onReorderFailure(failure);
      if (!ownsCurrentState && this.#pendingReorder === null && this.#latestOrderIntent !== null) {
        this.#pendingReorder = this.#latestOrderIntent;
      }
    }
    if (ownsCurrentState && failure !== null) {
      this.#restoreCurrentReorderRollback(intent);
    }
    this.#reconcileAfterReordersDrain = true;
  }

  #applySuccessfulReorderSettlement(
    intent: ReorderIntent,
    ownsCurrentState: boolean,
    catalog: ProjectLabelCatalog,
  ): void {
    if (ownsCurrentState) {
      this.#latestOrderIntent = null;
      this.#acceptAuthoritativeCatalog(catalog);
      return;
    }
    // A stale success confirms the server accepted this intent, but its
    // catalog snapshot predates a newer local catalog generation.
    if (this.#latestOrderIntent?.id === intent.id) {
      this.#latestOrderIntent = null;
    }
    this.#reconcileAfterReordersDrain = true;
  }

  #restoreCurrentReorderRollback(intent: ReorderIntent): void {
    const current = this.#currentCatalog();
    this.#latestOrderIntent = null;
    if (current !== undefined) {
      this.#writeLabels(projectCatalogLabelsInOrder(current.labels, intent.rollbackIDs, this.#projectID));
    }
    this.#catalogGeneration += 1;
  }

  #drain(): void {
    this.#startNextReorder();
    if (
      this.#reconcileAfterReordersDrain &&
      this.#inFlightReorder === null &&
      this.#pendingReorder === null
    ) {
      this.#reconcileAfterReordersDrain = false;
      this.#refreshNeeded = true;
    }
    this.#startRefreshIfIdle();
  }

  #startRefreshIfIdle(): void {
    if (this.#activeRead !== null || !this.#refreshNeeded) {
      return;
    }
    this.#refreshNeeded = false;
    void this.#queryClient
      .fetchQuery({
        queryKey: this.#queryKey,
        queryFn: async ({ signal }) => this.read(signal),
        staleTime: 0,
      })
      .catch(() => undefined);
  }
}

function sameLabelMembership(labels: readonly ProjectLabel[], labelIDs: readonly string[]): boolean {
  return labels.length === labelIDs.length && labels.every((label) => labelIDs.includes(label.id));
}

function assertExactCatalogPermutation(
  labels: readonly ProjectLabel[],
  labelIDs: readonly string[],
  projectID: string,
): void {
  if (!sameLabelMembership(labels, labelIDs) || new Set(labelIDs).size !== labelIDs.length) {
    throw new Error(`Label reorder is not an exact Project ${projectID} catalog permutation.`);
  }
}

function projectCatalogLabelsInOrder(
  labels: readonly ProjectLabel[],
  labelIDs: readonly string[],
  projectID: string,
): readonly ProjectLabel[] {
  const labelsByID = new Map(labels.map((label) => [label.id, label]));
  const ordered = labelIDs.map((labelID) => {
    const label = labelsByID.get(labelID);
    if (label === undefined) {
      throw new Error(`Project ${projectID} Label order references missing Label ${labelID}.`);
    }
    return label;
  });
  if (ordered.length !== labels.length) {
    throw new Error(`Project ${projectID} Label order omits current Label records.`);
  }
  return ordered;
}

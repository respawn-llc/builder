import type { TaskLabelAssignment } from "@/api";

const maxTaskLabels = 100;

export type TaskLabelUpdateInput = Readonly<{
  addLabelIDs: readonly string[];
  removeLabelIDs: readonly string[];
}>;

export type TaskLabelAssignmentFailure = Readonly<{
  labelID: string;
  desiredSelected: boolean;
  error: unknown;
}>;

export type TaskLabelAssignmentSnapshot = Readonly<{
  taskID: string;
  authoritativeLabelIDs: readonly string[];
  visibleLabelIDs: readonly string[];
  pendingLabelIDs: readonly string[];
  inFlightLabelID: string | null;
  failures: readonly TaskLabelAssignmentFailure[];
  dirty: boolean;
  reconciling: boolean;
  reconciliationFailure: TaskLabelReconciliationFailure | null;
  closed: boolean;
}>;

export type TaskLabelReconciliationFailure = Readonly<{
  error: unknown;
}>;

export type TaskLabelAssignmentController = Readonly<{
  getSnapshot(): TaskLabelAssignmentSnapshot;
  subscribe(listener: () => void): () => void;
  setDesired(labelID: string, selected: boolean): void;
  replaceAuthoritative(assignment: TaskLabelAssignment): void;
  readAuthoritative(): Promise<TaskLabelAssignment>;
  replaceAvailableLabelIDs(labelIDs: readonly string[]): void;
  retry(labelID: string): void;
  retryReconciliation(): void;
  markDirty(): void;
  deleteLabel(labelID: string): void;
  deleteTask(): void;
}>;

export function createTaskLabelAssignmentController({
  availableLabelIDs,
  initialLabelIDs,
  refetch,
  taskID,
  update,
}: Readonly<{
  availableLabelIDs: readonly string[];
  initialLabelIDs: readonly string[];
  refetch: () => Promise<TaskLabelAssignment>;
  taskID: string;
  update: (input: TaskLabelUpdateInput) => Promise<TaskLabelAssignment>;
}>): TaskLabelAssignmentController {
  return new TaskLabelAssignmentControllerImpl({
    availableLabelIDs,
    taskID,
    initialLabelIDs,
    update,
    refetch,
  });
}

type InFlightIntent = Readonly<{
  labelID: string;
  desiredSelected: boolean;
}>;

class TaskLabelAssignmentControllerImpl implements TaskLabelAssignmentController {
  readonly #taskID: string;
  readonly #update: (input: TaskLabelUpdateInput) => Promise<TaskLabelAssignment>;
  readonly #refetch: () => Promise<TaskLabelAssignment>;
  #availableLabelIDs: Set<string>;
  readonly #listeners = new Set<() => void>();
  readonly #pending = new Map<string, boolean>();
  readonly #failures = new Map<string, TaskLabelAssignmentFailure>();
  readonly #deletedLabelIDs = new Set<string>();
  #authoritative: Set<string>;
  #authoritativeGeneration = 0;
  #inFlight: InFlightIntent | null = null;
  #dirty = false;
  #reconciling = false;
  #reconciliationFailure: TaskLabelReconciliationFailure | null = null;
  #closed = false;
  #snapshot: TaskLabelAssignmentSnapshot;

  constructor({
    availableLabelIDs,
    initialLabelIDs,
    refetch,
    taskID,
    update,
  }: Readonly<{
    availableLabelIDs: readonly string[];
    initialLabelIDs: readonly string[];
    refetch: () => Promise<TaskLabelAssignment>;
    taskID: string;
    update: (input: TaskLabelUpdateInput) => Promise<TaskLabelAssignment>;
  }>) {
    this.#taskID = taskID;
    this.#update = update;
    this.#refetch = refetch;
    this.#availableLabelIDs = boundedLabelSet(availableLabelIDs, taskID, "available catalog");
    this.#authoritative = boundedLabelSet(initialLabelIDs, taskID, "initial assignment");
    for (const labelID of this.#authoritative) {
      if (!this.#availableLabelIDs.has(labelID)) {
        this.#authoritative.delete(labelID);
      }
    }
    this.#snapshot = this.#buildSnapshot();
  }

  getSnapshot(): TaskLabelAssignmentSnapshot {
    return this.#snapshot;
  }

  #buildSnapshot(): TaskLabelAssignmentSnapshot {
    const visible = new Set(this.#authoritative);
    for (const [labelID, selected] of this.#pending) {
      if (selected) {
        visible.add(labelID);
      } else {
        visible.delete(labelID);
      }
    }
    for (const labelID of this.#deletedLabelIDs) {
      visible.delete(labelID);
    }
    return {
      taskID: this.#taskID,
      authoritativeLabelIDs: [...this.#authoritative],
      visibleLabelIDs: [...visible],
      pendingLabelIDs: [...this.#pending.keys()],
      inFlightLabelID: this.#inFlight?.labelID ?? null,
      failures: [...this.#failures.values()].sort((left, right) => left.labelID.localeCompare(right.labelID)),
      dirty: this.#dirty,
      reconciling: this.#reconciling,
      reconciliationFailure: this.#reconciliationFailure,
      closed: this.#closed,
    };
  }

  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  }

  setDesired(labelID: string, selected: boolean): void {
    if (this.#closed || !this.#availableLabelIDs.has(labelID)) {
      return;
    }
    this.#failures.delete(labelID);
    this.#addPending(labelID, selected);
    this.#normalizePending();
    this.#emit();
    this.#drain();
  }

  replaceAuthoritative(assignment: TaskLabelAssignment): void {
    if (this.#closed) {
      return;
    }
    this.#authoritativeGeneration += 1;
    const previous = this.#authoritative;
    this.#applyAuthoritative(assignment);
    const hadReconciliationFailure = this.#reconciliationFailure !== null;
    this.#reconciliationFailure = null;
    if (labelSetsEqual(previous, this.#authoritative) && !hadReconciliationFailure) {
      return;
    }
    this.#normalizePending();
    this.#emit();
    this.#drain();
  }

  async readAuthoritative(): Promise<TaskLabelAssignment> {
    const generation = this.#authoritativeGeneration;
    const assignment = await this.#refetch();
    if (this.#closed || generation !== this.#authoritativeGeneration) {
      return this.#currentAssignment();
    }
    this.replaceAuthoritative(assignment);
    return this.#currentAssignment();
  }

  replaceAvailableLabelIDs(labelIDs: readonly string[]): void {
    if (this.#closed) {
      return;
    }
    const nextAvailableLabelIDs = boundedLabelSet(labelIDs, this.#taskID, "available catalog");
    if (labelSetsEqual(this.#availableLabelIDs, nextAvailableLabelIDs)) {
      return;
    }
    this.#availableLabelIDs = nextAvailableLabelIDs;
    for (const labelID of this.#authoritative) {
      if (!this.#availableLabelIDs.has(labelID)) {
        this.#authoritative.delete(labelID);
      }
    }
    for (const labelID of this.#pending.keys()) {
      if (!this.#availableLabelIDs.has(labelID)) {
        this.#pending.delete(labelID);
      }
    }
    for (const labelID of this.#failures.keys()) {
      if (!this.#availableLabelIDs.has(labelID)) {
        this.#failures.delete(labelID);
      }
    }
    this.#pruneUnavailableDeletedLabelIDs();
    this.#emit();
    this.#drain();
  }

  retry(labelID: string): void {
    const failure = this.#failures.get(labelID);
    if (failure === undefined || this.#closed || !this.#availableLabelIDs.has(labelID)) {
      return;
    }
    this.#addPending(labelID, failure.desiredSelected);
    this.#failures.delete(labelID);
    this.#emit();
    this.#drain();
  }

  retryReconciliation(): void {
    if (this.#closed || this.#reconciliationFailure === null) {
      return;
    }
    this.#reconciliationFailure = null;
    this.#dirty = true;
    this.#emit();
    this.#drain();
  }

  markDirty(): void {
    if (this.#closed) {
      return;
    }
    this.#dirty = true;
    this.#emit();
    this.#drain();
  }

  deleteLabel(labelID: string): void {
    if (this.#closed) {
      return;
    }
    this.#deletedLabelIDs.add(labelID);
    this.#pruneUnavailableDeletedLabelIDs();
    this.#authoritative.delete(labelID);
    this.#pending.delete(labelID);
    this.#failures.delete(labelID);
    this.#emit();
    this.#drain();
  }

  deleteTask(): void {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    this.#authoritative.clear();
    this.#pending.clear();
    this.#failures.clear();
    this.#inFlight = null;
    this.#reconciling = false;
    this.#reconciliationFailure = null;
    this.#dirty = false;
    this.#emit();
  }

  #normalizePending(): void {
    if (this.#inFlight !== null) {
      return;
    }
    for (const [labelID, selected] of this.#pending) {
      if (this.#authoritative.has(labelID) === selected) {
        this.#pending.delete(labelID);
      }
    }
  }

  #drain(): void {
    if (this.#closed || this.#inFlight !== null || this.#reconciling) {
      return;
    }
    this.#normalizePending();
    const next = this.#pending.entries().next();
    if (!next.done) {
      const [labelID, desiredSelected] = next.value;
      const intent = { labelID, desiredSelected };
      this.#inFlight = intent;
      this.#emit();
      void this.#runUpdate(intent);
      return;
    }
    if (this.#dirty && this.#reconciliationFailure === null) {
      this.#dirty = false;
      this.#reconciling = true;
      this.#emit();
      void this.#runRefetch();
    }
  }

  async #runUpdate(intent: InFlightIntent): Promise<void> {
    try {
      const assignment = await this.#update({
        addLabelIDs: intent.desiredSelected ? [intent.labelID] : [],
        removeLabelIDs: intent.desiredSelected ? [] : [intent.labelID],
      });
      if (!this.#closed) {
        this.#authoritativeGeneration += 1;
        this.#applyAuthoritative(assignment);
        if (this.#pending.get(intent.labelID) === intent.desiredSelected) {
          this.#pending.delete(intent.labelID);
        }
      }
    } catch (error: unknown) {
      if (!this.#closed && this.#pending.get(intent.labelID) === intent.desiredSelected) {
        this.#pending.delete(intent.labelID);
        this.#failures.set(intent.labelID, {
          labelID: intent.labelID,
          desiredSelected: intent.desiredSelected,
          error,
        });
      }
    } finally {
      if (!this.#closed) {
        this.#inFlight = null;
        this.#normalizePending();
        this.#emit();
        this.#drain();
      }
    }
  }

  async #runRefetch(): Promise<void> {
    try {
      await this.readAuthoritative();
      if (!this.#closed) {
        this.#reconciliationFailure = null;
      }
    } catch (error: unknown) {
      if (!this.#closed) {
        this.#reconciliationFailure = { error };
      }
    } finally {
      if (!this.#closed) {
        this.#reconciling = false;
        this.#emit();
        this.#drain();
      }
    }
  }

  #applyAuthoritative(assignment: TaskLabelAssignment): void {
    if (assignment.taskID !== this.#taskID) {
      throw new Error(
        `Task label assignment response for Task ${assignment.taskID} cannot update Task ${this.#taskID}.`,
      );
    }
    const next = boundedLabelSet(assignment.labelIDs, this.#taskID, "authoritative response");
    for (const labelID of next) {
      if (!this.#availableLabelIDs.has(labelID)) {
        next.delete(labelID);
      }
    }
    for (const labelID of this.#deletedLabelIDs) {
      next.delete(labelID);
    }
    this.#authoritative = next;
  }

  #currentAssignment(): TaskLabelAssignment {
    return {
      taskID: this.#taskID,
      labelIDs: [...this.#authoritative],
    };
  }

  #pruneUnavailableDeletedLabelIDs(): void {
    for (const labelID of this.#deletedLabelIDs) {
      if (!this.#availableLabelIDs.has(labelID)) {
        this.#deletedLabelIDs.delete(labelID);
      }
    }
  }

  #addPending(labelID: string, selected: boolean): void {
    const alreadyPending = this.#pending.has(labelID);
    if (!alreadyPending && this.#pending.size === maxTaskLabels) {
      throw new Error(
        `Task label assignment pending intents exceeded the 100-label bound for Task ${this.#taskID}.`,
      );
    }
    this.#pending.set(labelID, selected);
  }

  #emit(): void {
    this.#snapshot = this.#buildSnapshot();
    for (const listener of this.#listeners) {
      listener();
    }
  }
}

function boundedLabelSet(labelIDs: readonly string[], taskID: string, operation: string): Set<string> {
  if (labelIDs.length > maxTaskLabels) {
    throw new Error(`Task label assignment ${operation} exceeded the 100-label bound for Task ${taskID}.`);
  }
  const unique = new Set(labelIDs);
  if (unique.size !== labelIDs.length) {
    throw new Error(`Task label assignment ${operation} contains duplicate IDs for Task ${taskID}.`);
  }
  return unique;
}

function labelSetsEqual(left: ReadonlySet<string>, right: ReadonlySet<string>): boolean {
  if (left.size !== right.size) {
    return false;
  }
  for (const labelID of left) {
    if (!right.has(labelID)) {
      return false;
    }
  }
  return true;
}

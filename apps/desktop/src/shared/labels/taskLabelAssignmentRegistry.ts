import type { QueryClient } from "@tanstack/react-query";

import type { TaskLabelAssignment } from "@/api";
import { queryKeys } from "@/app-facade";
import {
  createTaskLabelAssignmentController,
  type TaskLabelAssignmentController,
  type TaskLabelUpdateInput,
} from "./taskLabelAssignmentController";
import { patchExistingTaskLabelAssignment, patchExistingTaskLabelProjections } from "./taskLabelCache";

export type TaskLabelAssignmentControllerLease = Readonly<{
  controller: TaskLabelAssignmentController;
  release(): void;
}>;

type ControllerInput = Readonly<{
  availableLabelIDs: readonly string[];
  initialAssignment: TaskLabelAssignment | null;
  refetch: () => Promise<TaskLabelAssignment>;
  taskID: string;
  update: (input: TaskLabelUpdateInput) => Promise<TaskLabelAssignment>;
}>;

interface RegistryEntry {
  controller: TaskLabelAssignmentController;
  references: number;
  stopCacheSync: () => void;
  stopCleanupWatch: (() => void) | null;
}

const registries = new WeakMap<QueryClient, TaskLabelAssignmentRegistry>();

export function taskLabelAssignmentRegistryFor(queryClient: QueryClient): TaskLabelAssignmentRegistry {
  const existing = registries.get(queryClient);
  if (existing !== undefined) {
    return existing;
  }
  const registry = new TaskLabelAssignmentRegistry(queryClient);
  registries.set(queryClient, registry);
  return registry;
}

class TaskLabelAssignmentRegistry {
  readonly #queryClient: QueryClient;
  readonly #entries = new Map<string, RegistryEntry>();
  readonly #listeners = new Map<string, Set<() => void>>();

  constructor(queryClient: QueryClient) {
    this.#queryClient = queryClient;
  }

  get(taskID: string): TaskLabelAssignmentController | null {
    return this.#entries.get(taskID)?.controller ?? null;
  }

  deleteLabel(labelID: string): void {
    for (const entry of this.#entries.values()) {
      entry.controller.deleteLabel(labelID);
    }
  }

  markDirty(taskID: string): boolean {
    const controller = this.#entries.get(taskID)?.controller;
    if (controller === undefined) {
      return false;
    }
    controller.markDirty();
    return true;
  }

  markAllDirty(): void {
    for (const entry of this.#entries.values()) {
      entry.controller.markDirty();
    }
  }

  deleteTask(taskID: string): void {
    const entry = this.#entries.get(taskID);
    if (entry === undefined) {
      return;
    }
    entry.controller.deleteTask();
    entry.stopCleanupWatch?.();
    entry.stopCleanupWatch = null;
    entry.stopCacheSync();
    entry.stopCacheSync = noOp;
    if (entry.references === 0) {
      this.#entries.delete(taskID);
      this.#emit(taskID);
    }
  }

  subscribe(taskID: string, listener: () => void): () => void {
    const listeners = this.#listeners.get(taskID) ?? new Set<() => void>();
    listeners.add(listener);
    this.#listeners.set(taskID, listeners);
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) {
        this.#listeners.delete(taskID);
      }
    };
  }

  acquire(input: ControllerInput): TaskLabelAssignmentControllerLease {
    const existing = this.#entries.get(input.taskID);
    if (existing !== undefined) {
      existing.stopCleanupWatch?.();
      existing.stopCleanupWatch = null;
      existing.references += 1;
      existing.controller.replaceAvailableLabelIDs(input.availableLabelIDs);
      return this.#lease(input.taskID, existing);
    }
    const controller = createTaskLabelAssignmentController({
      availableLabelIDs: input.availableLabelIDs,
      initialLabelIDs: input.initialAssignment?.labelIDs ?? [],
      refetch: input.refetch,
      taskID: input.taskID,
      update: input.update,
    });
    const entry: RegistryEntry = {
      controller,
      references: 1,
      stopCacheSync: this.#installCacheSync(input.taskID, controller),
      stopCleanupWatch: null,
    };
    this.#entries.set(input.taskID, entry);
    this.#emit(input.taskID);
    return this.#lease(input.taskID, entry);
  }

  #lease(taskID: string, entry: RegistryEntry): TaskLabelAssignmentControllerLease {
    let released = false;
    return {
      controller: entry.controller,
      release: () => {
        if (released) {
          return;
        }
        released = true;
        entry.references -= 1;
        this.#scheduleCleanup(taskID, entry);
      },
    };
  }

  #scheduleCleanup(taskID: string, entry: RegistryEntry): void {
    if (entry.references !== 0 || this.#entries.get(taskID) !== entry) {
      return;
    }
    if (!controllerHasUnsettledWork(entry.controller)) {
      entry.stopCacheSync();
      this.#entries.delete(taskID);
      this.#emit(taskID);
      return;
    }
    entry.stopCleanupWatch ??= entry.controller.subscribe(() => {
      if (entry.references !== 0 || controllerHasUnsettledWork(entry.controller)) {
        return;
      }
      entry.stopCleanupWatch?.();
      entry.stopCleanupWatch = null;
      if (this.#entries.get(taskID) === entry) {
        entry.stopCacheSync();
        this.#entries.delete(taskID);
        this.#emit(taskID);
      }
    });
  }

  #emit(taskID: string): void {
    for (const listener of this.#listeners.get(taskID) ?? []) {
      listener();
    }
  }

  #installCacheSync(taskID: string, controller: TaskLabelAssignmentController): () => void {
    let previousAuthoritativeLabelIDs = controller.getSnapshot().authoritativeLabelIDs;
    const sync = (): void => {
      const snapshot = controller.getSnapshot();
      if (snapshot.closed) {
        this.#queryClient.removeQueries({
          queryKey: queryKeys.taskLabels(taskID),
          exact: true,
        });
        return;
      }
      const authoritativeChanged = !labelIDListsEqual(
        previousAuthoritativeLabelIDs,
        snapshot.authoritativeLabelIDs,
      );
      previousAuthoritativeLabelIDs = snapshot.authoritativeLabelIDs;
      patchExistingTaskLabelAssignment(this.#queryClient, {
        taskID,
        labelIDs: snapshot.authoritativeLabelIDs,
      });
      patchExistingTaskLabelProjections(this.#queryClient, taskID, snapshot.visibleLabelIDs);
      if (authoritativeChanged) {
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.allBoards,
        });
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.allBoardNodeCards,
        });
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.allTaskLists,
        });
      }
    };
    sync();
    return controller.subscribe(sync);
  }
}

function controllerHasUnsettledWork(controller: TaskLabelAssignmentController): boolean {
  const snapshot = controller.getSnapshot();
  return (
    snapshot.inFlightLabelID !== null ||
    snapshot.pendingLabelIDs.length > 0 ||
    snapshot.dirty ||
    snapshot.reconciling
  );
}

function labelIDListsEqual(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((labelID, index) => labelID === right[index]);
}

function noOp(): void {
  return;
}

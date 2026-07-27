import type { QueryClient } from "@tanstack/react-query";

import { labelIDListsEqual, type TaskLabelAssignment } from "@/api";
import { queryKeys } from "@/app-facade";
import type { TaskLabelAssignmentController } from "./taskLabelAssignmentController";
import { patchExistingTaskLabelAssignment, patchExistingTaskLabelProjections } from "./taskLabelCache";

export type TaskLabelAssignmentControllerLease = Readonly<{
  controller: TaskLabelAssignmentController;
  release(): void;
}>;

type ControllerInput = Readonly<{
  availableLabelIDs: readonly string[];
  controller: TaskLabelAssignmentController;
  initialAssignment: TaskLabelAssignment;
  projectID: string;
  taskID: string;
  workflowID: string;
}>;

interface RegistryEntry {
  controller: TaskLabelAssignmentController;
  projectID: string;
  references: number;
  stopCacheSync: () => void;
  stopCleanupWatch: (() => void) | null;
  workflowID: string;
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

  deleteLabel(projectID: string, labelID: string): void {
    for (const entry of this.#entries.values()) {
      if (entry.projectID === projectID) {
        entry.controller.deleteLabel(labelID);
      }
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

  markProjectDirty(projectID: string): readonly string[] {
    const taskIDs: string[] = [];
    for (const [taskID, entry] of this.#entries) {
      if (entry.projectID === projectID) {
        entry.controller.markDirty();
        taskIDs.push(taskID);
      }
    }
    return taskIDs;
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
      if (existing.projectID !== input.projectID || existing.workflowID !== input.workflowID) {
        throw new Error(
          `Task ${input.taskID} label assignment scope changed while its controller is active.`,
        );
      }
      existing.stopCleanupWatch?.();
      existing.stopCleanupWatch = null;
      existing.references += 1;
      existing.controller.replaceAvailableLabelIDs(input.availableLabelIDs);
      existing.controller.replaceAuthoritative(input.initialAssignment);
      return this.#lease(input.taskID, existing);
    }
    const { controller } = input;
    const entry: RegistryEntry = {
      controller,
      projectID: input.projectID,
      references: 1,
      stopCacheSync: this.#installCacheSync(input, controller),
      stopCleanupWatch: null,
      workflowID: input.workflowID,
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

  #installCacheSync(input: ControllerInput, controller: TaskLabelAssignmentController): () => void {
    const { projectID, taskID, workflowID } = input;
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
      patchExistingTaskLabelProjections(this.#queryClient, taskID, snapshot.authoritativeLabelIDs);
      if (authoritativeChanged) {
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.boardWorkflowRoot(projectID, workflowID),
        });
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.boardNodeCardsWorkflowRoot(projectID, workflowID),
        });
        void this.#queryClient.invalidateQueries({
          queryKey: queryKeys.projectTaskListsRoot(projectID),
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

function noOp(): void {
  return;
}

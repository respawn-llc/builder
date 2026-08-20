import { useCallback, useRef, useState } from "react";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { decodeWorktreeSetupRetainedError, type WorktreeSetupRetainedError } from "@/api";
import { reportNonCancelledError } from "@/app-facade";
import {
  initialExecutionTargetSelectionDraft,
  taskInitiatingActionID,
  type ExecutionTargetSelectionDraft,
  type TaskInitiatingAction,
  type TaskInitiatingActionResult,
} from "./executionTargetContinuation";

export type PendingTaskInitiatingAction =
  | Readonly<{
      kind: "dependency_confirmation";
      action: TaskInitiatingAction;
      unsatisfiedDependencyCount: number;
    }>
  | Readonly<{
      kind: "execution_target";
      action: TaskInitiatingAction;
      requirement: WorkflowExecutionTargetSelectionRequirement;
      selection: ExecutionTargetSelectionDraft;
    }>
  | Readonly<{
      kind: "setup_recovery";
      action: Extract<TaskInitiatingAction, { kind: "move" }>;
      failure: WorktreeSetupRetainedError;
      retrySelection?: WorkflowExecutionTargetSelection;
    }>;

export type TaskInitiatingActionController = Readonly<{
  pending: PendingTaskInitiatingAction | null;
  running: boolean;
  run(action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void>;
  close(): void;
  selectMode(mode: WorkflowExecutionTargetSelectionMode): void;
  setCustomRef(customRef: string | null): void;
}>;

export function useTaskInitiatingActionController({
  execute,
  onApplied,
  onAppliedError,
}: Readonly<{
  execute: (
    action: TaskInitiatingAction,
    selection?: WorkflowExecutionTargetSelection,
  ) => Promise<TaskInitiatingActionResult>;
  onApplied: (result: TaskInitiatingActionResult) => void | Promise<void>;
  onAppliedError: (error: unknown) => void;
}>): TaskInitiatingActionController {
  const [pending, setPending] = useState<PendingTaskInitiatingAction | null>(null);
  const pendingRef = useRef<PendingTaskInitiatingAction | null>(null);
  const [running, setRunning] = useState(false);
  const scheduledRunsRef = useRef(new Map<string, Promise<void>>());
  const runTailRef = useRef<Promise<void>>(Promise.resolve());
  const updatePending = useCallback((next: PendingTaskInitiatingAction | null) => {
    pendingRef.current = next;
    setPending(next);
  }, []);

  const handleResult = useCallback(
    async (result: TaskInitiatingActionResult): Promise<void> => {
      if (result.response.outcome === "applied" || result.response.outcome === "no_op") {
        updatePending(null);
        try {
          await onApplied(result);
        } catch (error) {
          reportNonCancelledError(error, onAppliedError);
        }
        return;
      }
      if (result.response.outcome === "dependency_confirmation_required") {
        updatePending({
          kind: "dependency_confirmation",
          action: result.action,
          unsatisfiedDependencyCount: result.response.unsatisfiedDependencyCount,
        });
        return;
      }
      updatePending({
        kind: "execution_target",
        action: result.action,
        requirement: result.response.selectionRequired,
        selection: initialExecutionTargetSelectionDraft(result.response.selectionRequired),
      });
    },
    [onApplied, onAppliedError, updatePending],
  );

  const executeRun = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void> => {
      try {
        await handleResult(await execute(action, selection));
      } catch (error) {
        if (action.kind !== "move") throw error;
        const failure = decodeWorktreeSetupRetainedError(error);
        if (failure === null) throw error;
        updatePending({
          kind: "setup_recovery",
          action,
          failure,
          ...(selection === undefined ? {} : { retrySelection: selection }),
        });
      }
    },
    [execute, handleResult, updatePending],
  );
  const run = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void> => {
      const actionID = taskInitiatingActionID(action).toJSONValue();
      const scheduled = scheduledRunsRef.current.get(actionID);
      if (scheduled !== undefined) {
        return scheduled;
      }
      const executeScheduled = async () => {
        const pendingAction = pendingRef.current?.action;
        if (pendingAction !== undefined && taskInitiatingActionID(pendingAction).toJSONValue() !== actionID) {
          throw new Error("Finish or dismiss the pending Task action before starting another one.");
        }
        await executeRun(action, selection);
      };
      const operation =
        scheduledRunsRef.current.size === 0
          ? executeScheduled()
          : runTailRef.current.catch(() => undefined).then(executeScheduled);
      if (scheduledRunsRef.current.size === 0) {
        setRunning(true);
      }
      scheduledRunsRef.current.set(actionID, operation);
      runTailRef.current = operation;
      const settle = () => {
        if (scheduledRunsRef.current.get(actionID) === operation) {
          scheduledRunsRef.current.delete(actionID);
        }
        if (scheduledRunsRef.current.size === 0) {
          setRunning(false);
        }
      };
      void operation.then(settle, settle);
      return operation;
    },
    [executeRun],
  );

  const close = useCallback(() => {
    updatePending(null);
  }, [updatePending]);

  const selectMode = useCallback(
    (mode: WorkflowExecutionTargetSelectionMode) => {
      const current = pendingRef.current;
      if (current?.kind !== "execution_target") {
        return;
      }
      updatePending({
        ...current,
        selection: { ...current.selection, mode },
      });
    },
    [updatePending],
  );

  const setCustomRef = useCallback(
    (customRef: string | null) => {
      const current = pendingRef.current;
      if (current?.kind !== "execution_target") {
        return;
      }
      updatePending({
        ...current,
        selection: { ...current.selection, customRef },
      });
    },
    [updatePending],
  );

  return {
    pending,
    running,
    run,
    close,
    selectMode,
    setCustomRef,
  };
}

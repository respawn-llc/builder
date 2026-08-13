import { useCallback, useRef, useState } from "react";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import {
  decodeWorktreeSetupRetainedError,
  type WorktreeSetupRetainedError,
} from "@/api";
import {
  initialExecutionTargetSelectionDraft,
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
      kind: "setup_recovery"; action: Extract<TaskInitiatingAction, { kind: "move" }>;
      failure: WorktreeSetupRetainedError; retrySelection?: WorkflowExecutionTargetSelection;
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
  const initialRunRef = useRef<Promise<void> | null>(null);
  const initialActionRef = useRef<TaskInitiatingAction | null>(null);
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
          onAppliedError(error);
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
      setRunning(true);
      const operation = (async () => {
        try {
          await handleResult(await execute(action, selection));
        } catch (error) {
          if (action.kind !== "move") throw error;
          const failure = decodeWorktreeSetupRetainedError(error);
          if (failure === null) throw error;
          updatePending({ kind: "setup_recovery", action, failure,
            ...(selection === undefined ? {} : { retrySelection: selection }) });
        }
      })();
      initialRunRef.current = operation;
      initialActionRef.current = action;
      const settle = () => {
        if (initialRunRef.current === operation) {
          initialRunRef.current = null;
          initialActionRef.current = null;
          setRunning(false);
        }
      };
      void operation.then(settle, settle);
      await operation;
    },
    [execute, handleResult, updatePending],
  );
  const run = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void> => {
      const active = initialRunRef.current;
      if (active !== null) {
        if (initialActionRef.current === action) {
          await active;
          return;
        }
        await active;
        if (pendingRef.current !== null) {
          throw new Error("Finish or dismiss the pending Task action before starting another one.");
        }
      }
      return executeRun(action, selection);
    },
    [executeRun],
  );

  const close = useCallback(() => {
    updatePending(null);
  }, [updatePending]);

  const selectMode = useCallback((mode: WorkflowExecutionTargetSelectionMode) => {
    const current = pendingRef.current;
    if (current?.kind !== "execution_target") {
      return;
    }
    updatePending({
      ...current,
      selection: { ...current.selection, mode },
    });
  }, [updatePending]);

  const setCustomRef = useCallback((customRef: string | null) => {
    const current = pendingRef.current;
    if (current?.kind !== "execution_target") {
      return;
    }
    updatePending({
      ...current,
      selection: { ...current.selection, customRef },
    });
  }, [updatePending]);

  return {
    pending,
    running,
    run,
    close,
    selectMode,
    setCustomRef,
  };
}

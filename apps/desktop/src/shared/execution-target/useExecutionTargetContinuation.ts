import { useCallback, useRef, useState } from "react";

import type {
  WorktreeSetupRetainedError,
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { decodeWorktreeSetupRetainedError } from "@/api";
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
  const [running, setRunning] = useState(false);
  const initialRunRef = useRef<Promise<void> | null>(null);

  const handleResult = useCallback(
    async (result: TaskInitiatingActionResult): Promise<void> => {
      if (result.response.outcome === "applied" || result.response.outcome === "no_op") {
        setPending(null);
        try {
          await onApplied(result);
        } catch (error) {
          onAppliedError(error);
        }
        return;
      }
      if (result.response.outcome === "dependency_confirmation_required") {
        setPending({
          kind: "dependency_confirmation",
          action: result.action,
          unsatisfiedDependencyCount: result.response.unsatisfiedDependencyCount,
        });
        return;
      }
      setPending({
        kind: "execution_target",
        action: result.action,
        requirement: result.response.selectionRequired,
        selection: initialExecutionTargetSelectionDraft(result.response.selectionRequired),
      });
    },
    [onApplied, onAppliedError],
  );

  const run = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void> => {
      if (initialRunRef.current !== null) {
        await initialRunRef.current;
        return;
      }
      setRunning(true);
      const operation = (async () => {
        try {
          await handleResult(await execute(action, selection));
        } catch (error) {
          if (action.kind !== "move") throw error;
          const failure = decodeWorktreeSetupRetainedError(error);
          if (failure === null) throw error;
          setPending({ kind: "setup_recovery", action, failure,
            ...(selection === undefined ? {} : { retrySelection: selection }) });
        }
      })();
      initialRunRef.current = operation;
      const settle = () => {
        if (initialRunRef.current === operation) {
          initialRunRef.current = null;
          setRunning(false);
        }
      };
      void operation.then(settle, settle);
      await operation;
    },
    [execute, handleResult],
  );

  const close = useCallback(() => {
    setPending(null);
  }, []);

  const selectMode = useCallback((mode: WorkflowExecutionTargetSelectionMode) => {
    setPending((current) =>
      current?.kind !== "execution_target"
        ? current
        : {
            ...current,
            selection: { ...current.selection, mode },
          },
    );
  }, []);

  const setCustomRef = useCallback((customRef: string | null) => {
    setPending((current) =>
      current?.kind !== "execution_target"
        ? current
        : {
            ...current,
            selection: { ...current.selection, customRef },
          },
    );
  }, []);

  return {
    pending,
    running,
    run,
    close,
    selectMode,
    setCustomRef,
  };
}

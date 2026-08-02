import { useCallback, useRef, useState } from "react";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
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
    }>;

export type TaskInitiatingActionController = Readonly<{
  pending: PendingTaskInitiatingAction | null;
  running: boolean;
  run(action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<void>;
  close(): void;
  selectMode(mode: WorkflowExecutionTargetSelectionMode): void;
  setCustomRef(customRef: string): void;
}>;

export function useTaskInitiatingActionController({
  execute,
  onApplied,
  onAppliedError,
  onExecuteError,
}: Readonly<{
  execute: (
    action: TaskInitiatingAction,
    selection?: WorkflowExecutionTargetSelection,
  ) => Promise<TaskInitiatingActionResult>;
  onApplied: (result: TaskInitiatingActionResult) => void | Promise<void>;
  onAppliedError: (error: unknown) => void;
  onExecuteError?: (action: TaskInitiatingAction, error: unknown) => void;
}>): TaskInitiatingActionController {
  const [pending, setPending] = useState<PendingTaskInitiatingAction | null>(null);
  const [running, setRunning] = useState(false);
  const initialRunRef = useRef<Promise<void> | null>(null);

  const handleResult = useCallback(
    async (result: TaskInitiatingActionResult): Promise<void> => {
      if (result.response.outcome === "applied") {
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
          const result = await execute(action, selection);
          await handleResult(result);
        } catch (error) {
          setPending(null);
          if (onExecuteError !== undefined) {
            onExecuteError(action, error);
          } else {
            onAppliedError(error);
          }
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
    [execute, handleResult, onAppliedError, onExecuteError],
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

  const setCustomRef = useCallback((customRef: string) => {
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

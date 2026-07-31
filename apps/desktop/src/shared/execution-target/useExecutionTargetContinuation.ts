import { useCallback, useRef, useState } from "react";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { errorMessage } from "@/api";
import {
  executionTargetSelectionFromDraft,
  initialExecutionTargetSelectionDraft,
  proceedWithTaskInitiatingAction,
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
      phase: "ready" | "submitting" | "failed";
      error: string | null;
    }>;

export type TaskInitiatingActionController = Readonly<{
  pending: PendingTaskInitiatingAction | null;
  running: boolean;
  run(action: TaskInitiatingAction): Promise<void>;
  close(): void;
  proceed(): Promise<void>;
  submit(): Promise<void>;
  selectMode(mode: WorkflowExecutionTargetSelectionMode): void;
  setCustomRef(customRef: string): void;
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
  const submittingRef = useRef(false);

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
        phase: "ready",
        error: null,
      });
    },
    [onApplied, onAppliedError],
  );

  const run = useCallback(
    async (action: TaskInitiatingAction): Promise<void> => {
      if (initialRunRef.current !== null) {
        await initialRunRef.current;
        return;
      }
      setRunning(true);
      const operation = (async () => {
        const result = await execute(action);
        await handleResult(result);
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

  const proceed = useCallback(async (): Promise<void> => {
    if (pending?.kind !== "dependency_confirmation" || submittingRef.current) {
      return;
    }
    submittingRef.current = true;
    const action = proceedWithTaskInitiatingAction(pending.action);
    setRunning(true);
    try {
      await handleResult(await execute(action));
    } finally {
      submittingRef.current = false;
      setRunning(false);
    }
  }, [execute, handleResult, pending]);

  const submit = useCallback(async (): Promise<void> => {
    if (pending?.kind !== "execution_target" || submittingRef.current) {
      return;
    }
    const selection = executionTargetSelectionFromDraft(pending.selection);
    if (selection === null) {
      return;
    }
    submittingRef.current = true;
    setPending({ ...pending, phase: "submitting", error: null });
    let result: TaskInitiatingActionResult;
    try {
      result = await execute(pending.action, selection);
    } catch (error) {
      setPending({
        ...pending,
        phase: "failed",
        error: errorMessage(error),
      });
      submittingRef.current = false;
      return;
    }
    submittingRef.current = false;
    await handleResult(result);
  }, [execute, handleResult, pending]);

  const close = useCallback(() => {
    if (!submittingRef.current) {
      setPending(null);
    }
  }, []);

  const selectMode = useCallback((mode: WorkflowExecutionTargetSelectionMode) => {
    setPending((current) =>
      current?.kind !== "execution_target"
        ? current
        : {
            ...current,
            selection: { ...current.selection, mode },
            error: null,
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
            error: null,
          },
    );
  }, []);

  return {
    pending,
    running,
    run,
    close,
    proceed,
    submit,
    selectMode,
    setCustomRef,
  };
}

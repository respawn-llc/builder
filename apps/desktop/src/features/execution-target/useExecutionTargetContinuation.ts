import { useCallback, useRef, useState } from "react";

import type {
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "../../api";
import { errorMessage } from "../../api/errors";
import {
  executionTargetSelectionFromDraft,
  initialExecutionTargetSelectionDraft,
  type ExecutionTargetActionResult,
  type ExecutionTargetContinuationAction,
  type ExecutionTargetSelectionDraft,
} from "./executionTargetContinuation";

export type PendingExecutionTargetContinuation = Readonly<{
  action: ExecutionTargetContinuationAction;
  requirement: WorkflowExecutionTargetSelectionRequirement;
  selection: ExecutionTargetSelectionDraft;
  phase: "ready" | "submitting" | "failed";
  error: string | null;
}>;

export type ExecutionTargetContinuationController = Readonly<{
  pending: PendingExecutionTargetContinuation | null;
  running: boolean;
  run(action: ExecutionTargetContinuationAction): Promise<void>;
  close(): void;
  submit(): Promise<void>;
  selectMode(mode: WorkflowExecutionTargetSelectionMode): void;
  setCustomRef(customRef: string): void;
}>;

export function useExecutionTargetContinuation({
  execute,
  onApplied,
  onAppliedError,
}: Readonly<{
  execute: (
    action: ExecutionTargetContinuationAction,
    selection?: WorkflowExecutionTargetSelection,
  ) => Promise<ExecutionTargetActionResult>;
  onApplied: (result: ExecutionTargetActionResult) => void | Promise<void>;
  onAppliedError: (error: unknown) => void;
}>): ExecutionTargetContinuationController {
  const [pending, setPending] = useState<PendingExecutionTargetContinuation | null>(null);
  const [running, setRunning] = useState(false);
  const initialRunRef = useRef<Promise<void> | null>(null);
  const submittingRef = useRef(false);

  const handleResult = useCallback(
    async (result: ExecutionTargetActionResult): Promise<void> => {
      if (result.response.outcome === "applied") {
        setPending(null);
        try {
          await onApplied(result);
        } catch (error) {
          onAppliedError(error);
        }
        return;
      }
      setPending({
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
    async (action: ExecutionTargetContinuationAction): Promise<void> => {
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

  const submit = useCallback(async (): Promise<void> => {
    if (pending === null || submittingRef.current) {
      return;
    }
    const selection = executionTargetSelectionFromDraft(pending.selection);
    if (selection === null) {
      return;
    }
    submittingRef.current = true;
    setPending({ ...pending, phase: "submitting", error: null });
    let result: ExecutionTargetActionResult;
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
    if (result.response.outcome === "selection_required") {
      setPending({
        ...pending,
        requirement: result.response.selectionRequired,
        phase: "ready",
        error: null,
      });
      return;
    }
    setPending(null);
    try {
      await onApplied(result);
    } catch (error) {
      onAppliedError(error);
    }
  }, [execute, onApplied, onAppliedError, pending]);

  const close = useCallback(() => {
    if (!submittingRef.current) {
      setPending(null);
    }
  }, []);

  const selectMode = useCallback((mode: WorkflowExecutionTargetSelectionMode) => {
    setPending((current) =>
      current === null
        ? null
        : { ...current, selection: { ...current.selection, mode }, error: null },
    );
  }, []);

  const setCustomRef = useCallback((customRef: string) => {
    setPending((current) =>
      current === null
        ? null
        : { ...current, selection: { ...current.selection, customRef }, error: null },
    );
  }, []);

  return { pending, running, run, close, submit, selectMode, setCustomRef };
}

import { useCallback, useRef, useState } from "react";

import type {
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
      kind: "setup_recovery";
      action: Extract<TaskInitiatingAction, { kind: "move" | "resume" }>;
      failure: TaskSetupRecoveryFailure;
      targetIntent: TaskExecutionTargetIntent;
      selection: ExecutionTargetSelectionDraft | null;
    }>;

export type TaskExecutionTargetIntent =
  | Readonly<{ kind: "configured_policy" }>
  | Readonly<{ kind: "explicit_override"; selection: WorkflowExecutionTargetSelection }>;

export type TaskSetupRecoveryFailure = Readonly<{
  kind: "setup_script" | "target_preparation";
  diagnostic: string;
  scriptPath: string | null;
  retainedWorktree: Readonly<{ root: string }> | null;
  retainedPreviousWorktree: Readonly<{ root: string }> | null;
}>;

export type TaskInitiatingActionRunOutcome = "settled" | "setup_recovery";

export type TaskInitiatingActionController = Readonly<{
  pending: PendingTaskInitiatingAction | null;
  running: boolean;
  run(
    action: TaskInitiatingAction,
    selection?: WorkflowExecutionTargetSelection,
  ): Promise<TaskInitiatingActionRunOutcome>;
  openSetupRecovery(
    action: Extract<TaskInitiatingAction, { kind: "move" | "resume" }>,
    failure: TaskSetupRecoveryFailure,
    targetIntent?: TaskExecutionTargetIntent,
  ): void;
  chooseAnotherTarget(): void;
  close(): void;
  selectMode(mode: WorkflowExecutionTargetSelectionMode): void;
  setCustomRef(customRef: string): void;
}>;

export function useTaskInitiatingActionController({
  execute,
  onApplied,
  onAppliedError,
  onClosed,
}: Readonly<{
  execute: (
    action: TaskInitiatingAction,
    selection?: WorkflowExecutionTargetSelection,
  ) => Promise<TaskInitiatingActionResult>;
  onApplied: (result: TaskInitiatingActionResult) => void | Promise<void>;
  onAppliedError: (error: unknown) => void;
  onClosed?: (() => void) | undefined;
}>): TaskInitiatingActionController {
  const [pending, setPending] = useState<PendingTaskInitiatingAction | null>(null);
  const [running, setRunning] = useState(false);
  const initialRunRef = useRef<Promise<TaskInitiatingActionRunOutcome> | null>(null);

  const handleResult = useCallback(
    async (result: TaskInitiatingActionResult): Promise<TaskInitiatingActionRunOutcome> => {
      if (result.response.outcome === "applied" || result.response.outcome === "no_op") {
        setPending(null);
        try {
          await onApplied(result);
        } catch (error) {
          onAppliedError(error);
        }
        return "settled";
      }
      if (result.response.outcome === "dependency_confirmation_required") {
        setPending({
          kind: "dependency_confirmation",
          action: result.action,
          unsatisfiedDependencyCount: result.response.unsatisfiedDependencyCount,
        });
        return "settled";
      }
      setPending({
        kind: "execution_target",
        action: result.action,
        requirement: result.response.selectionRequired,
        selection: initialExecutionTargetSelectionDraft(result.response.selectionRequired),
      });
      return "settled";
    },
    [onApplied, onAppliedError],
  );

  const run = useCallback(
    async (
      action: TaskInitiatingAction,
      selection?: WorkflowExecutionTargetSelection,
    ): Promise<TaskInitiatingActionRunOutcome> => {
      if (initialRunRef.current !== null) {
        return initialRunRef.current;
      }
      setRunning(true);
      const operation = (async (): Promise<TaskInitiatingActionRunOutcome> => {
        try {
          const result = await execute(action, selection);
          return await handleResult(result);
        } catch (error) {
          if (action.kind !== "move") {
            setPending((current) => (current?.kind === "setup_recovery" ? null : current));
            throw error;
          }
          const setupFailure = decodeWorktreeSetupRetainedError(error);
          if (setupFailure === null) {
            setPending((current) => (current?.kind === "setup_recovery" ? null : current));
            throw error;
          }
          setPending({
            kind: "setup_recovery",
            action,
            failure: {
              kind: "setup_script",
              diagnostic: setupFailure.diagnostic,
              scriptPath: setupFailure.scriptPath,
              retainedWorktree: {
                root: setupFailure.worktree.registered.kent.canonicalRoot,
              },
              retainedPreviousWorktree:
                setupFailure.retainedPreviousWorktree === null
                  ? null
                  : {
                      root: setupFailure.retainedPreviousWorktree.worktree.registered.kent.canonicalRoot,
                    },
            },
            targetIntent:
              selection === undefined
                ? { kind: "configured_policy" }
                : { kind: "explicit_override", selection },
            selection: null,
          });
          return "setup_recovery";
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
      return operation;
    },
    [execute, handleResult],
  );

  const close = useCallback(() => {
    setPending(null);
    onClosed?.();
  }, [onClosed]);

  const openSetupRecovery = useCallback(
    (
      action: Extract<TaskInitiatingAction, { kind: "move" | "resume" }>,
      failure: TaskSetupRecoveryFailure,
      targetIntent: TaskExecutionTargetIntent = { kind: "configured_policy" },
    ) => {
      setPending({
        kind: "setup_recovery",
        action,
        failure,
        targetIntent,
        selection: null,
      });
    },
    [],
  );

  const chooseAnotherTarget = useCallback(() => {
    setPending((current) =>
      current?.kind !== "setup_recovery"
        ? current
        : {
            ...current,
            selection: initialExecutionTargetSelectionDraft({
              reason: "policy_requires_selection",
            }),
          },
    );
  }, []);

  const selectMode = useCallback((mode: WorkflowExecutionTargetSelectionMode) => {
    setPending((current) => {
      if (current?.kind === "execution_target") {
        return { ...current, selection: { ...current.selection, mode } };
      }
      if (current?.kind === "setup_recovery" && current.selection !== null) {
        return { ...current, selection: { ...current.selection, mode } };
      }
      return current;
    });
  }, []);

  const setCustomRef = useCallback((customRef: string) => {
    setPending((current) => {
      if (current?.kind === "execution_target") {
        return { ...current, selection: { ...current.selection, customRef } };
      }
      if (current?.kind === "setup_recovery" && current.selection !== null) {
        return { ...current, selection: { ...current.selection, customRef } };
      }
      return current;
    });
  }, []);

  return {
    pending,
    running,
    run,
    openSetupRecovery,
    chooseAnotherTarget,
    close,
    selectMode,
    setCustomRef,
  };
}

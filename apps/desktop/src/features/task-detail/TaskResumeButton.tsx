import { createContext, useCallback, useContext, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskSetupRecovery, type WorkflowExecutionTargetSelection } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
import {
  executeTaskInitiatingAction,
  resumeTaskInitiatingAction,
  startTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  type TaskSetupRecoveryFailure,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";
import { Button } from "@/ui";

type TaskInitiatingActionController = Readonly<{
  canonicalSetupOperationID: string | null;
  resume(recovery?: TaskSetupRecovery): void;
  start(): void;
  running: boolean;
}>;

const TaskInitiatingActionContext = createContext<TaskInitiatingActionController | null>(null);

export function TaskInitiatingActionProvider({
  children,
  onApplied,
  onViewDependencies,
  taskID,
  setupRecovery,
}: Readonly<{
  children: ReactNode;
  onApplied(): void | Promise<void>;
  onViewDependencies(taskID: string): void;
  taskID: string;
  setupRecovery: TaskSetupRecovery | null;
}>) {
  const { api } = useAppServices();
  const { push } = useStatusController();
  const { t } = useTranslation();
  const appliedActionKind = useRef<"resume" | "start">("resume");
  const reportError = useCallback(
    (kind: "resume" | "start", error: unknown) => {
      push({
        id: `task-${kind}-error`,
        title: t(kind === "resume" ? "board.resumeFailed" : "board.startFailed"),
        body: errorMessage(error),
        durationMs: Infinity,
        tone: "danger",
      });
    },
    [push, t],
  );
  const continuation = useTaskInitiatingActionController({
    execute: async (action, selection) => executeTaskInitiatingAction(api, action, selection),
    onApplied: async (result) => {
      if (result.kind === "resume" || result.kind === "start") {
        appliedActionKind.current = result.kind;
      }
      await onApplied();
    },
    onAppliedError: (error) => {
      reportError(appliedActionKind.current, error);
    },
  });
  function run(
    action: Extract<TaskInitiatingAction, { kind: "resume" | "start" }>,
    selection?: WorkflowExecutionTargetSelection,
  ): void {
    appliedActionKind.current = action.kind;
    void continuation.run(action, selection).catch((error: unknown) => {
      reportError(action.kind, error);
    });
  }
  function resume(recovery?: TaskSetupRecovery): void {
    if (recovery !== undefined) {
      continuation.openSetupRecovery(resumeTaskInitiatingAction(taskID), setupRecoveryFailure(recovery));
      return;
    }
    run(resumeTaskInitiatingAction(taskID));
  }
  function start(): void {
    run(startTaskInitiatingAction(taskID));
  }
  return (
    <TaskInitiatingActionContext.Provider
      value={{
        canonicalSetupOperationID: setupRecovery?.setupOperationID.toJSONValue() ?? null,
        resume,
        running: continuation.running,
        start,
      }}
    >
      {children}
      <TaskInitiatingActionDialogs
        continuation={continuation}
        onResult={(result) => {
          if (result.kind === "view_dependencies") {
            onViewDependencies(result.taskID);
          } else if (result.action.kind === "resume" || result.action.kind === "start") {
            run(result.action, result.selection);
          }
        }}
      />
    </TaskInitiatingActionContext.Provider>
  );
}

export function TaskResumeButton({
  disabled,
  setupRecovery,
}: Readonly<{ disabled: boolean; setupRecovery?: TaskSetupRecovery | undefined }>) {
  const { t } = useTranslation();
  const controller = useContext(TaskInitiatingActionContext);
  if (controller === null) {
    throw new Error("Task Resume button requires a Task initiating-action provider");
  }
  if (
    controller.canonicalSetupOperationID !== null &&
    controller.canonicalSetupOperationID !== setupRecovery?.setupOperationID.toJSONValue()
  ) {
    return null;
  }
  return (
    <Button
      data-testid="task-detail-resume"
      disabled={disabled || controller.running}
      onClick={() => {
        controller.resume(setupRecovery);
      }}
      variant="primary"
    >
      {t("board.resume")}
    </Button>
  );
}

export function TaskStartButton({ disabled }: Readonly<{ disabled: boolean }>) {
  const { t } = useTranslation();
  const controller = useContext(TaskInitiatingActionContext);
  if (controller === null) {
    throw new Error("Task Start button requires a Task initiating-action provider");
  }
  return (
    <Button
      data-testid="task-detail-start"
      disabled={disabled || controller.running}
      onClick={controller.start}
      variant="primary"
    >
      {t("task.start")}
    </Button>
  );
}

function setupRecoveryFailure(recovery: TaskSetupRecovery): TaskSetupRecoveryFailure {
  return {
    kind: recovery.cause === "target_preparation" ? "target_preparation" : "setup_script",
    diagnostic: recovery.diagnostic,
    scriptPath: null,
    retainedWorktree: recovery.retainedWorktree === null ? null : { root: recovery.retainedWorktree.root },
    retainedPreviousWorktree:
      recovery.retainedPreviousWorktree === null ? null : { root: recovery.retainedPreviousWorktree.root },
  };
}

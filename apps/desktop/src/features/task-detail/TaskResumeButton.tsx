import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskSetupRecovery, type WorkflowExecutionTargetSelection } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
import {
  executeTaskInitiatingAction,
  resumeTaskInitiatingAction,
  startTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";
import { Button } from "@/ui";

type TaskInitiatingActionController = Readonly<{
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
}: Readonly<{
  children: ReactNode;
  onApplied(): void | Promise<void>;
  onViewDependencies(taskID: string): void;
  taskID: string;
}>) {
  const { api } = useAppServices();
  const { push } = useStatusController();
  const { t } = useTranslation();
  const appliedActionKind = useRef<"resume" | "start">("resume");
  const [recovery, setRecovery] = useState<TaskSetupRecovery | null>(null);
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
      setRecovery(null);
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
  function resume(setupRecovery?: TaskSetupRecovery): void {
    if (setupRecovery !== undefined) {
      setRecovery(setupRecovery);
      return;
    }
    run(resumeTaskInitiatingAction(taskID));
  }
  function start(): void {
    run(startTaskInitiatingAction(taskID));
  }
  return (
    <TaskInitiatingActionContext.Provider value={{ resume, running: continuation.running, start }}>
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
        setupRecovery={
          recovery === null
            ? undefined
            : {
                onClose: () => {
                  setRecovery(null);
                },
                onSubmit: (selection) => {
                  run(resumeTaskInitiatingAction(taskID), selection);
                },
                recovery,
              }
        }
      />
    </TaskInitiatingActionContext.Provider>
  );
}

export function TaskResumeButton({
  disabled,
  recovery,
}: Readonly<{ disabled: boolean; recovery?: TaskSetupRecovery | undefined }>) {
  const { t } = useTranslation();
  const controller = useContext(TaskInitiatingActionContext);
  if (controller === null) {
    throw new Error("Task Resume button requires a Task initiating-action provider");
  }
  return (
    <Button
      data-testid="task-detail-resume"
      disabled={disabled || controller.running}
      onClick={() => {
        controller.resume(recovery);
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

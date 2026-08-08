import { createContext, useCallback, useContext, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type WorkflowExecutionTargetSelection } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
import {
  executeTaskInitiatingAction,
  resumeTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";
import { Button } from "@/ui";

type TaskResumeController = Readonly<{
  resume(): void;
  running: boolean;
}>;

const TaskResumeContext = createContext<TaskResumeController | null>(null);

export function TaskResumeProvider({
  children,
  onApplied,
  taskID,
}: Readonly<{
  children: ReactNode;
  onApplied(): void | Promise<void>;
  taskID: string;
}>) {
  const { api } = useAppServices();
  const { push } = useStatusController();
  const { t } = useTranslation();
  const reportError = useCallback(
    (error: unknown) => {
      push({
        id: "task-resume-error",
        title: t("board.resumeFailed"),
        body: errorMessage(error),
        durationMs: Infinity,
        tone: "danger",
      });
    },
    [push, t],
  );
  const continuation = useTaskInitiatingActionController({
    execute: async (action, selection) => executeTaskInitiatingAction(api, action, selection),
    onApplied,
    onAppliedError: reportError,
  });
  function run(
    action: Extract<TaskInitiatingAction, { kind: "resume" }>,
    selection?: WorkflowExecutionTargetSelection,
  ): void {
    void continuation.run(action, selection).catch(reportError);
  }
  function resume(): void {
    run(resumeTaskInitiatingAction(taskID));
  }
  return (
    <TaskResumeContext.Provider value={{ resume, running: continuation.running }}>
      {children}
      <TaskInitiatingActionDialogs
        continuation={continuation}
        onResult={(result) => {
          if (result.kind === "continue" && result.action.kind === "resume") {
            run(result.action, result.selection);
          }
        }}
      />
    </TaskResumeContext.Provider>
  );
}

export function TaskResumeButton({ disabled }: Readonly<{ disabled: boolean }>) {
  const { t } = useTranslation();
  const controller = useContext(TaskResumeContext);
  if (controller === null) {
    throw new Error("Task Resume button requires a Task Resume provider");
  }
  return (
    <Button
      data-testid="task-detail-resume"
      disabled={disabled || controller.running}
      onClick={controller.resume}
      variant="primary"
    >
      {t("board.resume")}
    </Button>
  );
}

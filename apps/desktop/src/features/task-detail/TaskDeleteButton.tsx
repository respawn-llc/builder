import { createContext, useContext, useRef, useState, type ReactNode } from "react";
import { Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { errorMessage, RpcError, rpcErrorCodes } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
import { TaskDeleteConfirmationDialog } from "@/shared/task-delete";
import { Button } from "@/ui";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";

type TaskDeleteController = Readonly<{
  running: boolean;
  open(): void;
}>;

const TaskDeleteContext = createContext<TaskDeleteController | null>(null);

export function TaskDeleteProvider({
  children,
  onDismiss,
  taskID,
}: Readonly<{
  children: ReactNode;
  onDismiss: TaskDetailDeleteDismissal;
  taskID: string;
}>) {
  const { api } = useAppServices();
  const { push } = useStatusController();
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const confirmationInFlight = useRef(false);

  async function deleteTask(): Promise<void> {
    setPending(true);
    try {
      try {
        await api.deleteTask(taskID);
      } catch (error) {
        if (!(error instanceof RpcError) || error.code !== rpcErrorCodes.workflowTaskNotFound) {
          push({
            body: errorMessage(error),
            id: "task-detail-delete-error",
            title: t("board.deleteTaskWindowError"),
            tone: "danger",
          });
          return;
        }
      }
      try {
        const dismissal = await onDismiss();
        if (dismissal.kind === "failed") {
          pushDismissalError(dismissal.error);
        }
      } catch (error) {
        pushDismissalError(error);
      }
    } finally {
      setPending(false);
      confirmationInFlight.current = false;
    }
  }

  function pushDismissalError(error: unknown): void {
    push({
      body: errorMessage(error),
      durationMs: Infinity,
      id: "task-detail-delete-dismiss-error",
      title: t("board.deleteTaskWindowError"),
      tone: "danger",
    });
  }

  return (
    <TaskDeleteContext.Provider
      value={{
        running: pending,
        open() {
          setOpen(true);
        },
      }}
    >
      {children}
      {open ? (
        <TaskDeleteConfirmationDialog
          disabled={false}
          onClose={() => {
            setOpen(false);
          }}
          onConfirm={() => {
            if (confirmationInFlight.current) {
              return;
            }
            confirmationInFlight.current = true;
            setOpen(false);
            queueMicrotask(() => {
              void deleteTask();
            });
          }}
        />
      ) : null}
    </TaskDeleteContext.Provider>
  );
}

export function TaskDeleteButton({
  active,
  disabled,
}: Readonly<{
  active: boolean;
  disabled: boolean;
}>) {
  const { t } = useTranslation();
  const controller = useContext(TaskDeleteContext);
  if (controller === null) {
    throw new Error("Task Delete button requires a Task Delete provider");
  }
  return (
    <Button
      aria-hidden={!active}
      aria-label={t("board.deleteTask")}
      className={`absolute inset-0 transition-opacity motion-reduce:transition-none ${
        active ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0 disabled:opacity-0!"
      }`}
      data-testid="task-detail-delete"
      disabled={disabled || controller.running || !active}
      onClick={controller.open}
      size="icon"
      tabIndex={active ? undefined : -1}
      title={active ? t("board.deleteTask") : undefined}
      variant="danger"
    >
      <Trash2 aria-hidden="true" size={16} strokeWidth={1.75} />
    </Button>
  );
}

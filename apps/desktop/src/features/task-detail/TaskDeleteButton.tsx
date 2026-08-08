import { createContext, useContext, useState, type ReactNode } from "react";
import { Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { errorMessage, RpcError, rpcErrorCodes } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
import { TaskDeleteConfirmationDialog } from "@/shared/task-delete";
import { Button } from "@/ui";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";

type TaskDeleteController = Readonly<{
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
  const [actionError, setActionError] = useState("");

  async function confirm(): Promise<void> {
    if (pending) {
      return;
    }
    setPending(true);
    setActionError("");
    try {
      await api.deleteTask(taskID);
    } catch (error) {
      if (!(error instanceof RpcError) || error.code !== rpcErrorCodes.workflowTaskNotFound) {
        setActionError(errorMessage(error));
        setPending(false);
        return;
      }
    }
    const dismissal = await onDismiss();
    if (dismissal.kind === "failed") {
      const message = errorMessage(dismissal.error);
      setActionError(message);
      setPending(false);
      push({
        body: message,
        durationMs: Infinity,
        id: "task-detail-delete-dismiss-error",
        title: t("board.deleteTaskWindowError"),
        tone: "danger",
      });
      return;
    }
    setOpen(false);
    setPending(false);
  }

  return (
    <TaskDeleteContext.Provider
      value={{
        open() {
          setActionError("");
          setOpen(true);
        },
      }}
    >
      {children}
      {open ? (
        <TaskDeleteConfirmationDialog
          actionError={actionError}
          disabled={pending}
          onClose={() => {
            if (!pending) {
              setOpen(false);
            }
          }}
          onConfirm={() => {
            void confirm();
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
        active ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0"
      }`}
      data-testid="task-detail-delete"
      disabled={disabled || !active}
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

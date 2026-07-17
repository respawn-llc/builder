import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { Button, Dialog } from "@/ui";
import { taskDeleteDialogWidth, type TaskDeleteTarget } from "./taskDeleteConfirmationModel";

export function TaskDeleteConfirmationFallbackDialog({
  disabled,
  onClose,
  onConfirm,
}: Readonly<{
  disabled: boolean;
  onClose: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onClose}
      open
      style={{ width: `min(${taskDeleteDialogWidth.toString()}px, calc(100vw - 32px))` }}
      title={t("board.deleteTaskTitle")}
    >
      <TaskDeleteConfirmationContent disabled={disabled} onCancel={onClose} onConfirm={onConfirm} />
    </Dialog>
  );
}

export function TaskDeleteWindowRoute({ taskID }: TaskDeleteTarget) {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const [actionError, setActionError] = useState("");
  const [pending, setPending] = useState(false);
  const submittedRef = useRef(false);

  async function confirmDelete(): Promise<void> {
    if (submittedRef.current) {
      return;
    }
    submittedRef.current = true;
    setPending(true);
    setActionError("");
    try {
      await api.deleteTask(taskID);
    } catch (error) {
      // Deletion itself failed: allow the operator to retry.
      submittedRef.current = false;
      setPending(false);
      const message = errorMessage(error);
      setActionError(message);
      push({
        id: "task-delete-window-error",
        tone: "danger",
        title: t("board.deleteTaskWindowError"),
        body: message,
      });
      return;
    }
    try {
      await nativeBridge.window.closeCurrent();
    } catch (error) {
      // Deletion already succeeded; surface the close failure without enabling a
      // retry that would target the now-missing task.
      push({
        id: "task-delete-window-close-error",
        tone: "danger",
        title: t("board.deleteTaskWindowCloseError"),
        body: errorMessage(error),
      });
    }
  }

  return (
    <NativeDialogWindow
      contentMaxWidth={`${taskDeleteDialogWidth.toString()}px`}
      title={t("board.deleteTaskTitle")}
    >
      <TaskDeleteConfirmationContent
        actionError={actionError}
        disabled={pending}
        onCancel={() => {
          void nativeBridge.window.closeCurrent();
        }}
        onConfirm={() => void confirmDelete()}
      />
    </NativeDialogWindow>
  );
}

function TaskDeleteConfirmationContent({
  actionError = "",
  disabled,
  onCancel,
  onConfirm,
}: Readonly<{
  actionError?: string | undefined;
  disabled: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t("board.deleteTaskBody")}</p>
      {actionError.length > 0 ? (
        <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-error)]">{actionError}</p>
      ) : null}
      <div className="grid grid-cols-2 gap-[var(--space-2)]">
        <Button className="w-full" disabled={disabled} onClick={onCancel}>
          {t("app.cancel")}
        </Button>
        <Button className="w-full" disabled={disabled} onClick={onConfirm} variant="danger">
          {t("board.deleteTaskConfirm")}
        </Button>
      </div>
    </div>
  );
}

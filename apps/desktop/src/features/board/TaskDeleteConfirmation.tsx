import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { invalidateAllTaskSearches } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { TaskDeleteConfirmationContent, taskDeleteDialogWidth } from "@/shared/task-delete";
import type { TaskDeleteTarget } from "./taskDeleteConfirmationModel";

export function TaskDeleteWindowRoute({ taskID }: TaskDeleteTarget) {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const queryClient = useQueryClient();
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
      await invalidateAllTaskSearches(queryClient);
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

import { useCallback } from "react";
import { useTranslation } from "react-i18next";

import { useStatusController } from "@/app-facade";
import { workflowTaskReadError } from "@/shared/workflow-task-read-error";

export function useBoardLoadErrorReporter(): (error: unknown) => void {
  const { t } = useTranslation();
  const { push } = useStatusController();
  return useCallback(
    (error: unknown) => {
      push({
        body: workflowTaskReadError(error, t).body,
        durationMs: Infinity,
        id: "board-load-error",
        title: t("board.loadFailed"),
        tone: "danger",
      });
    },
    [push, t],
  );
}

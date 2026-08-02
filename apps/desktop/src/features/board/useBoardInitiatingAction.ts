import { useCallback } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type ApiService } from "@/api";
import { useStatusController } from "@/app-facade";
import {
  executeTaskInitiatingAction,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";

export function useBoardInitiatingAction({
  api,
  onRefresh,
}: Readonly<{
  api: ApiService;
  onRefresh: () => Promise<void>;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const reportRefreshError = useCallback(
    (error: unknown) => {
      push({
        body: errorMessage(error),
        durationMs: Infinity,
        id: "board-action-refresh-error",
        title: t("board.loadFailed"),
        tone: "danger",
      });
    },
    [push, t],
  );
  const reportExecuteError = useCallback(
    (action: TaskInitiatingAction, error: unknown) => {
      const id =
        action.kind === "start"
          ? "board-start-error"
          : action.kind === "move"
            ? "board-move-error"
            : "board-resume-error";
      const title =
        action.kind === "start"
          ? t("board.startFailed")
          : action.kind === "move"
            ? t("board.moveFailed")
            : t("board.resumeFailed");
      push({ body: errorMessage(error), durationMs: Infinity, id, title, tone: "danger" });
    },
    [push, t],
  );
  return useTaskInitiatingActionController({
    execute: async (action, selection) => executeTaskInitiatingAction(api, action, selection),
    onApplied: onRefresh,
    onAppliedError: reportRefreshError,
    onExecuteError: reportExecuteError,
  });
}

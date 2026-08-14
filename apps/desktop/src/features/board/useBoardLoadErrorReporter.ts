import { useCallback } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { reportNonCancelledError, useStatusController } from "@/app-facade";

export function useBoardLoadErrorReporter(): (error: unknown) => void {
  const { t } = useTranslation();
  const { push } = useStatusController();
  return useCallback(
    (error: unknown) => {
      reportNonCancelledError(error, (failure) => {
        push({
          body: errorMessage(failure),
          durationMs: Infinity,
          id: "board-load-error",
          title: t("board.loadFailed"),
          tone: "danger",
        });
      });
    },
    [push, t],
  );
}

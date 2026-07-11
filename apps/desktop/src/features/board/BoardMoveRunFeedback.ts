import { useCallback, useRef } from "react";
import { useTranslation } from "react-i18next";

import { useStatusController } from "../../app/useStatusController";

export type BoardMoveRunTracker = Readonly<{
  observeInterruptedRun: (input: Readonly<{ runID: string; taskID: string }>) => void;
  trackMoveRunIDs: (result: Readonly<{ runIDs: readonly string[] }>) => void;
}>;

export function useBoardMoveRunFeedback(): BoardMoveRunTracker {
  const { t } = useTranslation();
  const { push } = useStatusController();
  // Pending move-run IDs never drive rendering, so they live in a ref. A ref also
  // makes track/observe synchronous: observe can atomically check-and-remove a run
  // ID instead of relying on a queued state updater that React may defer to a later
  // render (which would drop the toast or race two observations into duplicates).
  const pendingMoveRunIDsRef = useRef<Set<string>>(new Set());

  const trackMoveRunIDs = useCallback((result: Readonly<{ runIDs: readonly string[] }>): void => {
    for (const runID of result.runIDs) {
      const trimmed = runID.trim();
      if (trimmed.length > 0) {
        pendingMoveRunIDsRef.current.add(trimmed);
      }
    }
  }, []);

  const observeInterruptedRun = useCallback(
    (input: Readonly<{ runID: string; taskID: string }>): void => {
      const runID = input.runID.trim();
      // Set.delete returns true only for the call that actually removed the run ID,
      // so a tracked interruption notifies exactly once and untracked or repeated
      // observations are ignored.
      if (runID.length === 0 || !pendingMoveRunIDsRef.current.delete(runID)) {
        return;
      }
      push({
        id: `board-move-run-interrupted-${runID}`,
        tone: "danger",
        title: t("board.moveRunInterrupted"),
        body: t("board.moveRunInterruptedBody", { taskID: input.taskID }),
        durationMs: Infinity,
      });
    },
    [push, t],
  );

  return { observeInterruptedRun, trackMoveRunIDs };
}

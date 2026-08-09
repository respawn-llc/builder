import { useCallback } from "react";

import type { ApiService, WorkflowExecutionTargetSelection } from "@/api";
import {
  executeTaskInitiatingAction,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";

type BoardInitiatingActionControllerOptions = Readonly<{
  api: ApiService;
  connected: boolean;
  onActionError(id: string, title: string, error: unknown): void;
  onApplied(): void | Promise<void>;
  onPendingMoveChange(
    update: (current: PendingBoardCardMove | null) => PendingBoardCardMove | null,
  ): void;
  startErrorTitle: string;
  moveErrorTitle: string;
  refreshErrorTitle: string;
}>;

export function useBoardInitiatingActionController({
  api,
  connected,
  onActionError,
  onApplied,
  onPendingMoveChange,
  startErrorTitle,
  moveErrorTitle,
  refreshErrorTitle,
}: BoardInitiatingActionControllerOptions) {
  const execute = useCallback(
    async (action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection) =>
      executeTaskInitiatingAction(api, action, selection),
    [api],
  );
  const onAppliedError = useCallback(
    (error: unknown) => {
      onActionError("board-action-refresh-error", refreshErrorTitle, error);
    },
    [onActionError, refreshErrorTitle],
  );
  const initiatingAction = useTaskInitiatingActionController({
    execute,
    onApplied,
    onAppliedError,
  });
  const { pending, run, running } = initiatingAction;
  const clearPendingMove = useCallback(
    (pendingMove: PendingBoardCardMove) => {
      onPendingMoveChange((current) =>
        current?.taskID === pendingMove.taskID && current.targetColumnID === pendingMove.targetColumnID
          ? null
          : current,
      );
    },
    [onPendingMoveChange],
  );
  const runCardAction = useCallback(
    (
      action: TaskInitiatingAction,
      pendingMove: PendingBoardCardMove,
      selection?: WorkflowExecutionTargetSelection,
    ): void => {
      onPendingMoveChange(() => pendingMove);
      void run(action, selection)
        .catch((error: unknown) => {
          onActionError(
            action.kind === "start" ? "board-start-error" : "board-move-error",
            action.kind === "start" ? startErrorTitle : moveErrorTitle,
            error,
          );
        })
        .finally(() => {
          clearPendingMove(pendingMove);
        });
    },
    [
      clearPendingMove,
      moveErrorTitle,
      onActionError,
      onPendingMoveChange,
      run,
      startErrorTitle,
    ],
  );
  return {
    actionsDisabled: !connected || running || pending !== null,
    initiatingAction,
    runCardAction,
  };
}

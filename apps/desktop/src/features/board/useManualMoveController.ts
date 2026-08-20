import { useCallback, useRef, useState } from "react";

import type { ApiService, TaskMovePreviewResponse } from "@/api";
import { moveTaskInitiatingAction, type TaskInitiatingAction } from "@/shared/execution-target";
import type { ManualMoveDialogSubmit } from "./ManualMoveDialog";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";

export type PendingManualMove = Readonly<{
  id: number;
  taskID: string;
  targetNodeID: string;
  preview: TaskMovePreviewResponse;
}>;

type ManualMoveControllerOptions = Readonly<{
  api: Pick<ApiService, "previewMoveTask">;
  onPreviewBlocked(reason: string): void;
  onPreviewError(error: unknown): void;
  runAction(action: TaskInitiatingAction, pendingMove: PendingBoardCardMove): void;
}>;

export function useManualMoveController({
  api,
  onPreviewBlocked,
  onPreviewError,
  runAction,
}: ManualMoveControllerOptions) {
  const [pending, setPending] = useState<PendingManualMove | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const sequence = useRef(0);

  const cancel = useCallback(() => {
    sequence.current += 1;
    setPreviewing(false);
    setPending(null);
  }, []);

  const preview = useCallback(
    (taskID: string, targetNodeID: string): void => {
      const id = ++sequence.current;
      setPreviewing(true);
      setPending(null);
      void api
        .previewMoveTask(taskID, targetNodeID)
        .then((preview) => {
          if (id !== sequence.current || preview.outcome === "no_op") {
            return;
          }
          if (preview.outcome === "blocked") {
            onPreviewBlocked(preview.blocked.reason);
            return;
          }
          setPending({ id, taskID, targetNodeID, preview });
        })
        .catch((error: unknown) => {
          if (id === sequence.current) {
            onPreviewError(error);
          }
        })
        .finally(() => {
          if (id === sequence.current) {
            setPreviewing(false);
          }
        });
    },
    [api, onPreviewBlocked, onPreviewError],
  );

  const submit = useCallback(
    (input: ManualMoveDialogSubmit): void => {
      if (pending === null) {
        return;
      }
      const drop = pending;
      cancel();
      runAction(
        moveTaskInitiatingAction({
          taskID: drop.taskID,
          targetNodeID: drop.targetNodeID,
          ...(input.transitionKey === undefined ? {} : { transitionKey: input.transitionKey }),
          ...(input.values === undefined ? {} : { values: input.values }),
        }),
        { taskID: drop.taskID, targetColumnID: drop.targetNodeID },
      );
    },
    [cancel, pending, runAction],
  );

  return {
    actionsDisabled: previewing || pending !== null,
    cancel,
    pending,
    preview,
    submit,
  };
}

import { useCallback, useLayoutEffect, useState, type RefObject } from "react";

import type { BoardColumn } from "@/api";
import type { KanbanCardVM } from "./BoardColumnViewModel";
import { useBoardDragAutoScroll } from "./BoardDragAutoScroll";
import { classifyDrop } from "./BoardDropActions";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import type { BoardColumnDropState } from "./BoardDragTypes";
import type { BoardCardInstance } from "./BoardCardInstance";

export type ActiveBoardCardDrag = Readonly<{
  instance: BoardCardInstance;
  lastCardIndex: number;
  payload: BoardCardDragPayload;
  snapshot: KanbanCardVM;
}>;

export function classifyBoardColumnDropState({
  column,
  drag,
  dragBlocked,
  firstActiveID,
}: Readonly<{
  column: BoardColumn;
  drag: ActiveBoardCardDrag | null;
  dragBlocked: boolean;
  firstActiveID: string | undefined;
}>): BoardColumnDropState {
  if (drag === null) {
    return dragBlocked ? "blocked" : "idle";
  }
  return classifyDrop(column, drag.payload, firstActiveID).kind === "reject" ? "blocked" : "idle";
}

export function useBoardDragLifecycle({
  disabled,
  rootRef,
}: Readonly<{
  disabled: boolean;
  rootRef: RefObject<HTMLDivElement | null>;
}>) {
  const [candidateDrag, setCandidateDrag] = useState<ActiveBoardCardDrag | null>(null);
  const activeDrag = disabled ? null : candidateDrag;
  const autoScroll = useBoardDragAutoScroll({ active: activeDrag !== null, rootRef });
  const stopAutoScroll = autoScroll.stop;
  const cancel = useCallback(() => {
    stopAutoScroll();
    setCandidateDrag(null);
  }, [stopAutoScroll]);
  const start = useCallback(
    (drag: ActiveBoardCardDrag) => {
      if (!disabled) {
        setCandidateDrag(drag);
      }
    },
    [disabled],
  );
  useLayoutEffect(() => {
    if (!disabled || candidateDrag === null) {
      return;
    }
    stopAutoScroll();
    queueMicrotask(() => {
      setCandidateDrag((current) => (current === candidateDrag ? null : current));
    });
  }, [candidateDrag, disabled, stopAutoScroll]);
  return {
    activeDrag,
    autoScroll,
    cancel,
    dragBlocked: disabled && candidateDrag !== null,
    start,
  };
}

import type { KanbanCardVM } from "./BoardColumnViewModel";
import type { BoardCardDragPayload } from "./BoardDragTypes";
import type { BoardCardInstance } from "./BoardCardInstance";

export type ActiveBoardCardDrag = Readonly<{
  instance: BoardCardInstance;
  lastVirtualIndex: number;
  payload: BoardCardDragPayload;
  snapshot: KanbanCardVM;
}>;

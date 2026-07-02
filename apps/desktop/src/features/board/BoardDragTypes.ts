import { z } from "zod";

export const boardCardDragPayloadType = "application/x-board-card+json";

export type BoardCardDragPayload = Readonly<{
  taskID: string;
  canStart: boolean;
  activeNodeIDs: readonly string[];
  statusKind: string;
  manualMoveTargetNodeIDs: readonly string[];
}>;

export type BoardColumnDropState = "idle" | "allowed" | "blocked";

const boardCardDragPayloadSchema = z.object({
  taskID: z.string().min(1),
  canStart: z.boolean(),
  activeNodeIDs: z.array(z.string().min(1)),
  statusKind: z.string().min(1),
  manualMoveTargetNodeIDs: z.array(z.string().min(1)),
});

export function encodeBoardCardDragPayload(payload: BoardCardDragPayload): string {
  return JSON.stringify({
    taskID: payload.taskID,
    canStart: payload.canStart,
    activeNodeIDs: [...payload.activeNodeIDs],
    statusKind: payload.statusKind,
    manualMoveTargetNodeIDs: [...payload.manualMoveTargetNodeIDs],
  });
}

export function decodeBoardCardDragPayload(serialized: string): BoardCardDragPayload | null {
  if (serialized.length === 0) {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(serialized);
  } catch {
    return null;
  }
  const payload = boardCardDragPayloadSchema.safeParse(parsed);
  if (!payload.success) {
    return null;
  }
  return {
    taskID: payload.data.taskID,
    canStart: payload.data.canStart,
    activeNodeIDs: payload.data.activeNodeIDs,
    statusKind: payload.data.statusKind,
    manualMoveTargetNodeIDs: payload.data.manualMoveTargetNodeIDs,
  };
}

import { createUUIDv4ValueParser, type UUIDv4Value } from "./setupOperationID";

export type WorktreeOperationID = UUIDv4Value<"worktree_operation">;
export const parseWorktreeOperationID = createUUIDv4ValueParser<"worktree_operation">(
  "Worktree operation id must be a UUID v4.",
);

import type { SidebarTaskDetailSnapshot } from "@/app-facade";

export type TaskDetailSidebarState = Readonly<{
  activationID: string;
  snapshot?: SidebarTaskDetailSnapshot;
}>;

export function taskDetailSidebarState(
  activationID: string | null | undefined,
  snapshot: SidebarTaskDetailSnapshot | undefined,
): TaskDetailSidebarState | undefined {
  if (activationID === null || activationID === undefined) {
    return undefined;
  }
  return snapshot === undefined ? { activationID } : { activationID, snapshot };
}

export type TaskDetailSavePendingState = Readonly<{
  task: boolean;
  addComment: boolean;
  editComment: boolean;
}>;

export type TaskDetailSnapshotInput = Omit<SidebarTaskDetailSnapshot, "kind">;

export function taskDetailSavePending(state: TaskDetailSavePendingState): boolean {
  return state.task || state.addComment || state.editComment;
}

export function taskDetailSnapshot(input: TaskDetailSnapshotInput): SidebarTaskDetailSnapshot {
  return {
    kind: "taskDetail",
    scrollTop: Math.max(0, input.scrollTop),
    descriptionExpanded: input.descriptionExpanded,
    selectedTab: input.selectedTab,
    ...(input.titleBodyDraft === undefined ? {} : { titleBodyDraft: input.titleBodyDraft }),
    ...(input.newCommentDraft === undefined || input.newCommentDraft.length === 0
      ? {}
      : { newCommentDraft: input.newCommentDraft }),
    ...(input.editedCommentDraft === undefined ? {} : { editedCommentDraft: input.editedCommentDraft }),
  };
}

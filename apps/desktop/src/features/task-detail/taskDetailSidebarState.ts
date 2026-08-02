import type { SidebarTaskDetailSnapshot } from "@/app-facade";

export type TaskDetailSavePendingState = Readonly<{
  task: boolean;
  addComment: boolean;
  editComment: boolean;
}>;

export type TaskDetailSnapshotInput = Readonly<{
  scrollTop: number;
  descriptionExpanded: boolean;
  selectedTab: "comments" | "activity";
  titleBodyDraft?: Readonly<{ title: string; body: string }> | undefined;
  newCommentDraft?: string | undefined;
  editedCommentDraft?: Readonly<{ commentID: string; body: string }> | undefined;
}>;

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

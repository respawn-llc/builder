import type { ActivityItem, TaskComment, TaskDetail } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import { taskDetailInitialFocusRequestKey } from "@/app-facade";
import type { VirtualizedPixelOffsetRequest } from "@/ui";
import type { TaskDraft } from "./TaskDetailRows";
import type { DetailTab } from "./TaskDetailTabs";

export function resolveTaskDetailFocusRequestKey(
  taskID: string,
  focus: TaskDetailInitialFocus | undefined,
  requestKey: string | undefined,
): string | undefined {
  if (requestKey !== undefined) {
    return requestKey;
  }
  return focus === undefined ? undefined : taskDetailInitialFocusRequestKey(taskID, focus);
}

export function taskDetailDraftState({
  detail,
  disabled,
  draft,
  updatePending,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  updatePending: boolean;
}>): Readonly<{ canSaveDraft: boolean; draftDirty: boolean }> {
  const draftDirty = draft.title !== detail.title || draft.body !== detail.body;
  return {
    canSaveDraft: draftDirty && !disabled && !updatePending && draft.title.trim().length > 0,
    draftDirty,
  };
}

export function resolveTaskDetailInitialScrollKey(
  initialFocus: TaskDetailInitialFocus | undefined,
): string | undefined {
  if (initialFocus === undefined) {
    return undefined;
  }
  return initialFocus.kind === "dependencies" ? "dependencies" : "inbox";
}

export function resolveFirstFeedItemKey(
  selectedTab: DetailTab,
  activityItems: readonly Readonly<{ presentationKey: string; item: ActivityItem }>[],
  commentItems: readonly Readonly<{ presentationKey: string; item: TaskComment }>[],
): string | undefined {
  return (selectedTab === "comments" ? commentItems : activityItems)[0]?.presentationKey;
}

export function resolveFeedPixelOffsetRequest({
  activityPending,
  attentionPending,
  commentsPending,
  pixelOffsetRequest,
  selectedTab,
}: Readonly<{
  activityPending: boolean;
  attentionPending: boolean;
  commentsPending: boolean;
  pixelOffsetRequest: VirtualizedPixelOffsetRequest | undefined;
  selectedTab: DetailTab;
}>): VirtualizedPixelOffsetRequest | undefined {
  if (attentionPending || (selectedTab === "comments" ? commentsPending : activityPending)) {
    return undefined;
  }
  return pixelOffsetRequest;
}

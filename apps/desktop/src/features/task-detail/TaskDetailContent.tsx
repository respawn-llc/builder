import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { errorMessage, type TaskDependencyDirection, type TaskDetail } from "@/api";
import type {
  SidebarPageNavigator,
  SidebarMode,
  SidebarRootController,
  SidebarDestination,
  TaskDetailInitialFocus,
} from "@/app-facade";
import { useAppNavigation, useConnectionSnapshot, useStatusController } from "@/app-facade";
import { useUpdateTask } from "@/shared/task-mutations";
import { createVirtualizedPixelOffsetRequest } from "@/ui";
import {
  initialDescriptionPresentationState,
  type DescriptionPresentationState,
} from "./TaskDetailDescriptionPresentation";
import { TaskDeleteProvider } from "./TaskDeleteButton";
import { TaskDetailList } from "./TaskDetailList";
import { taskDetailSidebarDestination } from "./taskDetailSidebarDestination";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import { TaskInitiatingActionProvider, type TaskResumeAuthority } from "./TaskResumeButton";
import type { TaskDraft } from "./TaskDetailRows";
import { useTaskMutations, useTaskDetailLiveRefresh } from "./useTaskDetailData";
import type { useTaskActivity, useTaskAttention, useTaskComments } from "./useTaskDetailData";

// TaskDraftState tracks the editable title/body draft alongside the server
// snapshot (`base`) the draft last synced to. Comparing the draft to `base`
// distinguishes genuine unsaved user edits from a draft that merely lags behind
// a server refresh, which lets a clean surface follow live updates while a
// dirty surface keeps the user's in-progress edits.
type TaskDraftState = Readonly<{
  taskID: string;
  base: TaskDraft;
  draft: TaskDraft;
}>;

export function TaskDetailContent({
  activity,
  attention,
  comments,
  detail,
  initialFocus,
  onDeleteDismiss,
  onMutated,
  navigator,
  retainedState,
  sidebarDestination,
  sidebarMode,
  openSidebar,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  attention: ReturnType<typeof useTaskAttention>;
  comments: ReturnType<typeof useTaskComments>;
  detail: TaskDetail;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onDeleteDismiss: TaskDetailDeleteDismissal;
  onMutated?: (() => void) | undefined;
  navigator?: SidebarPageNavigator | undefined;
  openSidebar?: SidebarRootController["open"] | undefined;
  retainedState?: unknown;
  sidebarDestination?: Extract<SidebarDestination, { kind: "taskDetail" }> | undefined;
  sidebarMode?: SidebarMode | undefined;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const navigation = useAppNavigation();
  const relationshipNavigationAvailable = hasRelationshipNavigation(navigator, openSidebar);
  const restored = decodeTaskDetailRetainedState(retainedState);
  const restorationKey = useId();
  const [scrollElement, setScrollElement] = useState<HTMLDivElement | null>(null);
  const pixelOffsetRequest = useMemo(
    () =>
      restored === undefined
        ? undefined
        : createVirtualizedPixelOffsetRequest(restorationKey, restored.scrollOffsetPx),
    [restorationKey, restored],
  );
  const serverDraft = taskDraft(detail);
  const [draftState, setDraftState] = useState<TaskDraftState>(() => ({
    taskID: detail.id,
    base: restored?.base ?? serverDraft,
    draft: restored?.draft ?? serverDraft,
  }));
  const [editingComment, setEditingComment] = useState<Readonly<{ id: string; body: string }> | null>(
    restored?.editingComment ?? null,
  );
  const [newCommentBody, setNewCommentBody] = useState(restored?.newCommentBody ?? "");
  const [descriptionPresentation, setDescriptionPresentation] = useState<DescriptionPresentationState>(
    restored?.descriptionPresentation ?? initialDescriptionPresentationState,
  );
  const [selectedTab, setSelectedTab] = useState<"comments" | "activity">(
    restored?.selectedTab ?? "comments",
  );
  const [localDependencyFocusRequest, setLocalDependencyFocusRequest] = useState<number | null>(null);
  const [questionSelections, setQuestionSelections] = useState<ReadonlyMap<string, QuestionSelectionState>>(
    () => new Map(),
  );
  // When the surface switches to a different task, drop the previous task's
  // in-progress comment edit, new-comment draft, and question selections so they
  // don't bleed into the newly loaded task. Reset during render (the React
  // "adjust state on prop change" pattern) rather than in an effect. The
  // title/body draft is reconciled separately below via reconcileDraftState.
  const [loadedTaskID, setLoadedTaskID] = useState(detail.id);
  if (loadedTaskID !== detail.id) {
    setLoadedTaskID(detail.id);
    setEditingComment(null);
    setNewCommentBody("");
    setDescriptionPresentation(initialDescriptionPresentationState);
    setQuestionSelections(new Map());
    setLocalDependencyFocusRequest(null);
  }
  const update = useUpdateTask(detail.id, detail.projectID);
  useTaskDetailRetainedCapture({
    base: draftState.base,
    descriptionPresentation,
    draft: draftState.draft,
    editingComment,
    navigator,
    newCommentBody,
    scrollElement,
    selectedTab,
  });
  const reportActionError = useCallback(
    (action: "dependency_remove" | "interrupt", error: unknown) => {
      const notice =
        action === "interrupt"
          ? { id: "task-interrupt-error", title: t("board.interruptFailed") }
          : {
              id: "task-dependency-remove-error",
              title: t("task.dependenciesRemoveFailed"),
            };
      push({
        ...notice,
        body: errorMessage(error),
        durationMs: action === "dependency_remove" ? 5000 : Infinity,
        tone: "danger",
      });
    },
    [push, t],
  );
  const mutations = useTaskMutations(detail.id, detail.projectID, {
    onActionError: reportActionError,
    onChanged: onMutated,
  });
  const connection = useConnectionSnapshot();
  useTaskDetailLiveRefresh(detail, true);
  const resumeAuthority = taskResumeAuthority(attention);

  // Reconcile the draft with the latest server snapshot during render (the
  // React "adjust state on prop change" pattern). Switching tasks resets to the
  // server values; a clean surface follows live server updates; a surface with
  // unsaved edits keeps the user's draft so a background refresh never clobbers
  // in-progress work. A draft that has caught up to the server (e.g. after a
  // save) re-baselines so subsequent server changes are followed again.
  const reconciled = reconcileDraftState(draftState, detail.id, serverDraft);
  if (reconciled !== draftState) {
    setDraftState(reconciled);
  }
  const draft = reconciled.draft;
  const focusPresentation = taskDetailFocusPresentation({
    initialFocus,
    localDependencyFocusRequest,
    restored: restored !== undefined,
  });

  async function saveDraft(nextDraft: TaskDraft = draft): Promise<void> {
    await update.mutateAsync({
      taskID: detail.id,
      title: nextDraft.title,
      body: nextDraft.body,
    });
    onMutated?.();
  }

  return (
    <TaskInitiatingActionProvider
      authority={resumeAuthority}
      key={detail.id}
      onApplied={mutations.refresh}
      onViewDependencies={(taskID) => {
        presentTaskDependencies({
          navigator,
          openSidebar,
          requestLocalFocus: () => {
            setLocalDependencyFocusRequest((current) => (current === null ? 1 : current + 1));
          },
          sidebarDestination,
          taskID,
        });
      }}
      taskID={detail.id}
    >
      <TaskDeleteProvider onDismiss={onDeleteDismiss} taskID={detail.id}>
        <TaskDetailList
          activity={activity}
          attention={attention}
          comments={comments}
          detail={detail}
          disabled={connection.phase !== "connected"}
          draft={draft}
          descriptionPresentation={descriptionPresentation}
          editingComment={editingComment}
          focusRequestKey={
            localDependencyFocusRequest === null
              ? undefined
              : `${detail.id}:dependencies:${localDependencyFocusRequest.toString()}`
          }
          initialFocus={focusPresentation}
          mutations={mutations}
          newCommentBody={newCommentBody}
          relationshipNavigationAvailable={relationshipNavigationAvailable}
          onDraftChange={(nextDraft) => {
            setDraftState({ taskID: detail.id, base: reconciled.base, draft: nextDraft });
          }}
          onDescriptionPresentationChange={setDescriptionPresentation}
          onAddDependency={(direction) => {
            openRelatedTaskCreation({ detail, direction, navigator, openSidebar });
          }}
          onRemoveDependency={(pair) => {
            mutations.removeDependency.mutate(pair);
          }}
          onSelectDependencyTask={(taskID) => {
            if (navigator !== undefined) {
              navigator.push(
                sidebarDestination === undefined
                  ? dependencyDestination(taskID, sidebarMode)
                  : taskDetailSidebarDestination(sidebarDestination, taskID),
              );
              return;
            }
            void navigation.replaceTask(taskID);
          }}
          onNewCommentBodyChange={setNewCommentBody}
          onEditingCommentChange={setEditingComment}
          onQuestionSelectionChange={(askID, selection) => {
            setQuestionSelections((previous) => new Map(previous).set(askID, selection));
          }}
          onScrollElementChange={setScrollElement}
          onSaveDraft={saveDraft}
          pixelOffsetRequest={pixelOffsetRequest}
          questionSelections={questionSelections}
          selectedTab={selectedTab}
          setTab={setSelectedTab}
          updateError={update.error}
          updatePending={update.isPending}
        />
      </TaskDeleteProvider>
    </TaskInitiatingActionProvider>
  );
}

function taskResumeAuthority(attention: ReturnType<typeof useTaskAttention>): TaskResumeAuthority {
  if (!attention.isSuccess || attention.isFetching) {
    return { kind: "unavailable" };
  }
  for (const item of attention.data.items) {
    if (item.kind === "interrupted_current_node" && item.setupRecovery !== null) {
      return { kind: "setup_recovery", recovery: item.setupRecovery };
    }
  }
  return { kind: "ordinary" };
}

function taskDetailFocusPresentation({
  initialFocus,
  localDependencyFocusRequest,
  restored,
}: Readonly<{
  initialFocus: TaskDetailInitialFocus | undefined;
  localDependencyFocusRequest: number | null;
  restored: boolean;
}>): TaskDetailInitialFocus | undefined {
  if (localDependencyFocusRequest !== null) {
    return { kind: "dependencies" };
  }
  return restored ? undefined : initialFocus;
}

function presentTaskDependencies({
  navigator,
  openSidebar,
  requestLocalFocus,
  sidebarDestination,
  taskID,
}: Readonly<{
  navigator: SidebarPageNavigator | undefined;
  openSidebar: SidebarRootController["open"] | undefined;
  requestLocalFocus(): void;
  sidebarDestination: Extract<SidebarDestination, { kind: "taskDetail" }> | undefined;
  taskID: string;
}>): void {
  if (navigator !== undefined && sidebarDestination !== undefined) {
    navigator.replace(taskDetailSidebarDestination(sidebarDestination, taskID, { kind: "dependencies" }));
    return;
  }
  if (openSidebar !== undefined) {
    openSidebar({
      kind: "taskDetail",
      initialFocus: { kind: "dependencies" },
      taskID,
    });
    return;
  }
  requestLocalFocus();
}

function dependencyDestination(
  taskID: string,
  mode: SidebarMode | undefined,
): Extract<SidebarDestination, { kind: "taskDetail" }> {
  return {
    kind: "taskDetail",
    taskID,
    ...(mode === undefined ? {} : { mode }),
  };
}

function openRelatedTaskCreation({
  detail,
  direction,
  navigator,
  openSidebar,
}: Readonly<{
  detail: TaskDetail;
  direction: TaskDependencyDirection;
  navigator?: SidebarPageNavigator | undefined;
  openSidebar?: SidebarRootController["open"] | undefined;
}>) {
  const destination = {
    boardQueryWorkflowID: detail.workflowID,
    initialSourceWorkspaceID: detail.sourceWorkspace.id,
    kind: "newTask" as const,
    mode: "overlay" as const,
    pendingRelationship: {
      originTaskID: detail.id,
      newTaskRole: direction === "blocked-by" ? ("blocker" as const) : ("blocked" as const),
    },
    projectID: detail.projectID,
    workflowID: detail.workflowID,
  };
  if (navigator !== undefined) {
    navigator.push(destination);
    return;
  }
  openSidebar?.(destination);
}

function hasRelationshipNavigation(
  navigator: SidebarPageNavigator | undefined,
  openSidebar: SidebarRootController["open"] | undefined,
): boolean {
  return navigator !== undefined || openSidebar !== undefined;
}

function useTaskDetailRetainedCapture({
  base,
  descriptionPresentation,
  draft,
  editingComment,
  navigator,
  newCommentBody,
  scrollElement,
  selectedTab,
}: Readonly<{
  base: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  draft: TaskDraft;
  editingComment: Readonly<{ id: string; body: string }> | null;
  navigator?: SidebarPageNavigator | undefined;
  newCommentBody: string;
  scrollElement: HTMLDivElement | null;
  selectedTab: "comments" | "activity";
}>) {
  useEffect(() => {
    if (navigator === undefined || scrollElement === null) return;
    return navigator.registerCapture(() => ({
      base,
      descriptionPresentation,
      draft,
      editingComment,
      newCommentBody,
      scrollOffsetPx: scrollElement.scrollTop,
      selectedTab,
    }));
  }, [
    base,
    descriptionPresentation,
    draft,
    editingComment,
    navigator,
    newCommentBody,
    scrollElement,
    selectedTab,
  ]);
}

type TaskDetailRetainedState = Readonly<{
  base: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  draft: TaskDraft;
  editingComment: Readonly<{ id: string; body: string }> | null;
  newCommentBody: string;
  scrollOffsetPx: number;
  selectedTab: "comments" | "activity";
}>;

const taskDetailRetainedStateSchema = z.object({
  base: z.object({ body: z.string(), title: z.string() }),
  descriptionPresentation: z.object({ editing: z.boolean(), expanded: z.boolean() }),
  draft: z.object({ body: z.string(), title: z.string() }),
  editingComment: z.object({ body: z.string(), id: z.string() }).nullable(),
  newCommentBody: z.string(),
  scrollOffsetPx: z.number().nonnegative(),
  selectedTab: z.enum(["comments", "activity"]),
});

function decodeTaskDetailRetainedState(state: unknown): TaskDetailRetainedState | undefined {
  return taskDetailRetainedStateSchema.safeParse(state).data;
}

function canonicalSetupRecovery(items: readonly AttentionItem[]): TaskSetupRecovery | null {
  for (const item of items) {
    if (item.kind === "interrupted_current_node" && item.setupRecovery !== null) {
      return item.setupRecovery;
    }
  }
  return null;
}

function taskDraft(detail: TaskDetail): TaskDraft {
  return { title: detail.title, body: detail.body };
}

function sameDraft(a: TaskDraft, b: TaskDraft): boolean {
  return a.title === b.title && a.body === b.body;
}

function reconcileDraftState(state: TaskDraftState, taskID: string, serverDraft: TaskDraft): TaskDraftState {
  if (state.taskID !== taskID) {
    // Switched to a different task: drop the previous task's draft entirely.
    return { taskID, base: serverDraft, draft: serverDraft };
  }
  const hasUnsavedEdits = !sameDraft(state.draft, state.base);
  if (!hasUnsavedEdits) {
    // Clean surface: track the latest server values (re-baseline on change).
    return sameDraft(state.base, serverDraft) ? state : { taskID, base: serverDraft, draft: serverDraft };
  }
  if (sameDraft(state.draft, serverDraft)) {
    // The draft caught up to the server (e.g. the edit was just saved): treat
    // it as clean again so future server changes are followed.
    return { taskID, base: serverDraft, draft: serverDraft };
  }
  // Unsaved edits diverge from the server: keep them; edits take priority.
  return state;
}

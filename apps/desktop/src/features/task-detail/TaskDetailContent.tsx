import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskDetail } from "@/api";
import type { SidebarDestination, SidebarTaskDetailSnapshot, TaskDetailInitialFocus } from "@/app-facade";
import { useAppNavigation, useConnectionSnapshot, useSidebar, useStatusController } from "@/app-facade";
import { useUpdateTask } from "@/shared/task-mutations";
import {
  initialDescriptionPresentationState,
  type DescriptionPresentationState,
} from "./TaskDetailDescriptionPresentation";
import { TaskDetailList } from "./TaskDetailList";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import type { TaskDraft } from "./TaskDetailRows";
import { useTaskMutations, useTaskDetailLiveRefresh } from "./useTaskDetailData";
import type { useTaskActivity, useTaskAttention, useTaskComments } from "./useTaskDetailData";
import { taskDetailSavePending, taskDetailSnapshot } from "./taskDetailSidebarState";

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

function taskDetailSidebarSnapshot(
  snapshot: SidebarTaskDetailSnapshot | undefined,
): SidebarTaskDetailSnapshot | undefined {
  return snapshot?.kind === "taskDetail" ? snapshot : undefined;
}

function relatedNewTaskDestination(
  detail: TaskDetail,
  direction: "blocked-by" | "blocks",
): Extract<SidebarDestination, { kind: "newTask" }> {
  return {
    boardQueryWorkflowID: detail.workflowID,
    initialSourceWorkspaceID: detail.sourceWorkspace.id,
    kind: "newTask",
    mode: "overlay",
    pendingRelationship: {
      originTaskID: detail.id,
      newTaskRole: direction === "blocked-by" ? "blocker" : "blocked",
    },
    projectID: detail.projectID,
    workflowID: detail.workflowID,
  };
}

function relatedTaskDestination(
  current: Extract<SidebarDestination, { kind: "taskDetail" }>,
  taskID: string,
): Extract<SidebarDestination, { kind: "taskDetail" }> {
  return {
    kind: "taskDetail",
    taskID,
    ...(current.mode === undefined ? {} : { mode: current.mode }),
    ...(current.onMutated === undefined ? {} : { onMutated: current.onMutated }),
  };
}

function initialTaskDraftState(
  taskID: string,
  serverDraft: TaskDraft,
  snapshot: SidebarTaskDetailSnapshot | undefined,
): TaskDraftState {
  return { taskID, base: serverDraft, draft: snapshot?.titleBodyDraft ?? serverDraft };
}

function restoredEditingComment(
  snapshot: SidebarTaskDetailSnapshot | undefined,
): Readonly<{ id: string; body: string }> | null {
  return snapshot?.editedCommentDraft === undefined
    ? null
    : { id: snapshot.editedCommentDraft.commentID, body: snapshot.editedCommentDraft.body };
}

function restoredDescriptionPresentation(
  snapshot: SidebarTaskDetailSnapshot | undefined,
): DescriptionPresentationState {
  return snapshot === undefined
    ? initialDescriptionPresentationState
    : { editing: false, expanded: snapshot.descriptionExpanded };
}

function useTaskDetailSidebarCapture({
  activeToken,
  descriptionExpanded,
  editingComment,
  newCommentBody,
  registerSidebarStateCapture,
  savePending,
  scrollElementRef,
  selectedTab,
  titleBodyDraft,
}: Readonly<{
  activeToken: ReturnType<typeof useSidebar>["activeToken"];
  descriptionExpanded: boolean;
  editingComment: Readonly<{ id: string; body: string }> | null;
  newCommentBody: string;
  registerSidebarStateCapture: ReturnType<typeof useSidebar>["registerSidebarStateCapture"];
  savePending: boolean;
  scrollElementRef: Readonly<{ current: HTMLDivElement | null }>;
  selectedTab: "comments" | "activity";
  titleBodyDraft: TaskDraft | undefined;
}>) {
  useEffect(() => {
    if (activeToken === null) return;
    return registerSidebarStateCapture(activeToken, () =>
      savePending
        ? null
        : taskDetailSnapshot({
            descriptionExpanded,
            editedCommentDraft:
              editingComment === null
                ? undefined
                : { body: editingComment.body, commentID: editingComment.id },
            newCommentDraft: newCommentBody,
            scrollTop: scrollElementRef.current?.scrollTop ?? 0,
            selectedTab,
            titleBodyDraft,
          }),
    );
  }, [
    activeToken,
    descriptionExpanded,
    editingComment,
    newCommentBody,
    registerSidebarStateCapture,
    savePending,
    scrollElementRef,
    selectedTab,
    titleBodyDraft,
  ]);
}

function useTaskDetailNavigation({
  activeDestination,
  detail,
  navigation,
  openSidebar,
  pushSidebar,
}: Readonly<{
  activeDestination: SidebarDestination | null;
  detail: TaskDetail;
  navigation: ReturnType<typeof useAppNavigation>;
  openSidebar: ReturnType<typeof useSidebar>["openSidebar"];
  pushSidebar: ReturnType<typeof useSidebar>["pushSidebar"];
}>) {
  const addDependency = useCallback(
    (direction: "blocked-by" | "blocks") => {
      const destination = relatedNewTaskDestination(detail, direction);
      if (activeDestination?.kind === "taskDetail") pushSidebar(destination);
      else void openSidebar(destination);
    },
    [activeDestination, detail, openSidebar, pushSidebar],
  );
  const selectDependencyTask = useCallback(
    (taskID: string) => {
      if (activeDestination?.kind === "taskDetail") {
        pushSidebar(relatedTaskDestination(activeDestination, taskID));
        return;
      }
      void navigation.replaceTask(taskID);
    },
    [activeDestination, navigation, pushSidebar],
  );
  return { addDependency, selectDependencyTask };
}

function taskDetailRenderState({
  detail,
  draftState,
  loadedTaskID,
  serverDraft,
  snapshot,
}: Readonly<{
  detail: TaskDetail;
  draftState: TaskDraftState;
  loadedTaskID: string;
  serverDraft: TaskDraft;
  snapshot: SidebarTaskDetailSnapshot | undefined;
}>): Readonly<{ changed: boolean; state: TaskDraftState }> {
  if (loadedTaskID !== detail.id) {
    return {
      changed: true,
      state: initialTaskDraftState(detail.id, serverDraft, snapshot),
    };
  }
  return {
    changed: false,
    state: reconcileDraftState(draftState, detail.id, serverDraft),
  };
}

function useTaskDetailLocalState(
  detail: TaskDetail,
  serverDraft: TaskDraft,
  snapshot: SidebarTaskDetailSnapshot | undefined,
) {
  const [draftState, setDraftState] = useState<TaskDraftState>(() =>
    initialTaskDraftState(detail.id, serverDraft, snapshot),
  );
  const [editingComment, setEditingComment] = useState<Readonly<{ id: string; body: string }> | null>(
    () => restoredEditingComment(snapshot),
  );
  const [newCommentBody, setNewCommentBody] = useState(snapshot?.newCommentDraft ?? "");
  const [descriptionPresentation, setDescriptionPresentation] = useState<DescriptionPresentationState>(
    () => restoredDescriptionPresentation(snapshot),
  );
  const [selectedTab, setSelectedTab] = useState<"comments" | "activity">(
    snapshot?.selectedTab ?? "comments",
  );
  const [questionSelections, setQuestionSelections] = useState<ReadonlyMap<string, QuestionSelectionState>>(
    () => new Map(),
  );
  const [loadedTaskID, setLoadedTaskID] = useState(detail.id);
  const { changed: taskChanged, state: reconciled } = taskDetailRenderState({
    detail,
    draftState,
    loadedTaskID,
    serverDraft,
    snapshot,
  });
  if (taskChanged) {
    setLoadedTaskID(detail.id);
    setEditingComment(restoredEditingComment(snapshot));
    setNewCommentBody(snapshot?.newCommentDraft ?? "");
    setDescriptionPresentation(restoredDescriptionPresentation(snapshot));
    setDraftState(reconciled);
    setSelectedTab(snapshot?.selectedTab ?? "comments");
    setQuestionSelections(new Map());
  }
  if (reconciled !== draftState) setDraftState(reconciled);
  return {
    draft: reconciled.draft,
    draftState: reconciled,
    editingComment,
    newCommentBody,
    descriptionPresentation,
    questionSelections,
    selectedTab,
    setDraftState,
    setEditingComment,
    setNewCommentBody,
    setDescriptionPresentation,
    setQuestionSelections,
    setSelectedTab,
  };
}

export function TaskDetailContent({
  activity,
  attention,
  comments,
  detail,
  initialFocus,
  onMutated,
  openLink,
  sidebarActivationID,
  restoredDataReady,
  sidebarSnapshot,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  attention: ReturnType<typeof useTaskAttention>;
  comments: ReturnType<typeof useTaskComments>;
  detail: TaskDetail;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onMutated?: (() => void) | undefined;
  openLink: (url: string) => void;
  sidebarActivationID?: string | null | undefined;
  restoredDataReady: boolean;
  sidebarSnapshot?: SidebarTaskDetailSnapshot | undefined;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const navigation = useAppNavigation();
  const {
    activeDestination,
    activeToken,
    pushSidebar,
    registerSidebarStateCapture,
    openSidebar,
  } = useSidebar();
  const serverDraft = taskDraft(detail);
  const restoredSnapshot = taskDetailSidebarSnapshot(sidebarSnapshot);
  const scrollElementRef = useRef<HTMLDivElement | null>(null);
  const {
    draft,
    draftState,
    editingComment,
    newCommentBody,
    descriptionPresentation,
    questionSelections,
    selectedTab,
    setDraftState,
    setEditingComment,
    setNewCommentBody,
    setDescriptionPresentation,
    setQuestionSelections,
    setSelectedTab,
  } = useTaskDetailLocalState(detail, serverDraft, restoredSnapshot);
  const update = useUpdateTask(detail.id);
  const reportActionError = useCallback(
    (action: "dependency_remove" | "interrupt" | "resume", error: unknown) => {
      const notice =
        action === "interrupt"
          ? { id: "task-interrupt-error", title: t("board.interruptFailed") }
          : action === "resume"
            ? { id: "task-resume-error", title: t("board.resumeFailed") }
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
  const savePending = taskDetailSavePending({
    addComment: mutations.addComment.isPending,
    editComment: mutations.replaceComment.isPending,
    task: update.isPending,
  });
  const connection = useConnectionSnapshot();
  useTaskDetailLiveRefresh(detail, true);

  // Reconcile the draft with the latest server snapshot during render (the
  // React "adjust state on prop change" pattern). Switching tasks resets to the
  // server values; a clean surface follows live server updates; a surface with
  // unsaved edits keeps the user's draft so a background refresh never clobbers
  // in-progress work. A draft that has caught up to the server (e.g. after a
  // save) re-baselines so subsequent server changes are followed again.
  const reconciled = draftState;

  useTaskDetailSidebarCapture({
    activeToken,
    descriptionExpanded: descriptionPresentation.expanded,
    editingComment,
    newCommentBody,
    registerSidebarStateCapture,
    savePending,
    scrollElementRef,
    selectedTab,
    titleBodyDraft: sameDraft(reconciled.draft, reconciled.base) ? undefined : reconciled.draft,
  });

  async function saveDraft(nextDraft: TaskDraft = draft): Promise<void> {
    await update.mutateAsync({
      taskID: detail.id,
      title: nextDraft.title,
      body: nextDraft.body,
    });
    onMutated?.();
  }
  const { addDependency, selectDependencyTask } = useTaskDetailNavigation({
    activeDestination,
    detail,
    navigation,
    openSidebar,
    pushSidebar,
  });

  return (
    <TaskDetailList
      activity={activity}
      attention={attention}
      comments={comments}
      detail={detail}
      disabled={connection.phase !== "connected"}
      dependencyDisabled={connection.phase !== "connected" || savePending}
      dependencyRemoveDisabled={connection.phase !== "connected"}
      draft={draft}
      descriptionPresentation={descriptionPresentation}
      editingComment={editingComment}
      initialFocus={initialFocus}
      mutations={mutations}
      newCommentBody={newCommentBody}
      onDraftChange={(nextDraft) => {
        setDraftState({ taskID: detail.id, base: reconciled.base, draft: nextDraft });
      }}
      onDescriptionPresentationChange={setDescriptionPresentation}
      onAddDependency={addDependency}
      onRemoveDependency={(pair) => {
        mutations.removeDependency.mutate(pair);
      }}
      onSelectDependencyTask={selectDependencyTask}
      onNewCommentBodyChange={setNewCommentBody}
      onEditingCommentChange={setEditingComment}
      onQuestionSelectionChange={(askID, selection) => {
        setQuestionSelections((previous) => new Map(previous).set(askID, selection));
      }}
      onSaveDraft={saveDraft}
      openLink={openLink}
      questionSelections={questionSelections}
      selectedTab={selectedTab}
      setTab={setSelectedTab}
      updateError={update.error}
      updatePending={update.isPending}
      initialScrollOffset={restoredSnapshot?.scrollTop}
      initialScrollOffsetRequestKey={
        activeToken === null ||
        sidebarActivationID === null ||
        sidebarActivationID === undefined ||
        restoredSnapshot === undefined ||
        !restoredDataReady
          ? undefined
          : `${sidebarActivationID}:${detail.id}:${restoredSnapshot.scrollTop.toString()}`
      }
      onScrollElementChange={(element) => {
        scrollElementRef.current = element;
      }}
    />
  );
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

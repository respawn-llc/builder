import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type TaskDetail } from "@/api";
import type { TaskDetailInitialFocus } from "@/app-facade";
import {
  useAppNavigation,
  useAppServices,
  useConnectionSnapshot,
  useSidebar,
  useStatusController,
} from "@/app-facade";
import {
  TaskInitiatingActionDialogs,
  executeTaskInitiatingAction,
  type TaskInitiatingActionDialogResult,
  useTaskInitiatingActionController,
} from "@/shared/execution-target";
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
  onMutated,
  openLink,
}: Readonly<{
  activity: ReturnType<typeof useTaskActivity>;
  attention: ReturnType<typeof useTaskAttention>;
  comments: ReturnType<typeof useTaskComments>;
  detail: TaskDetail;
  initialFocus?: TaskDetailInitialFocus | undefined;
  onMutated?: (() => void) | undefined;
  openLink: (url: string) => void;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const { api } = useAppServices();
  const navigation = useAppNavigation();
  const { activeDestination, openSidebar, replaceSidebar } = useSidebar();
  const serverDraft = taskDraft(detail);
  const [draftState, setDraftState] = useState<TaskDraftState>(() => ({
    taskID: detail.id,
    base: serverDraft,
    draft: serverDraft,
  }));
  const [editingComment, setEditingComment] = useState<Readonly<{ id: string; body: string }> | null>(null);
  const [newCommentBody, setNewCommentBody] = useState("");
  const [descriptionPresentation, setDescriptionPresentation] = useState<DescriptionPresentationState>(
    initialDescriptionPresentationState,
  );
  const [selectedTab, setSelectedTab] = useState<"comments" | "activity">("comments");
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
  }
  const update = useUpdateTask(detail.id);
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
  const resumeContinuation = useTaskInitiatingActionController({
    execute: async (action, selection) => executeTaskInitiatingAction(api, action, selection),
    onApplied: async () => {
      onMutated?.();
    },
    onAppliedError: (error) => {
      push({
        id: "task-resume-error",
        title: t("board.resumeFailed"),
        body: errorMessage(error),
        durationMs: Infinity,
        tone: "danger",
      });
    },
  });
  const connection = useConnectionSnapshot();
  useTaskDetailLiveRefresh(detail, true);

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

  async function saveDraft(nextDraft: TaskDraft = draft): Promise<void> {
    await update.mutateAsync({
      taskID: detail.id,
      title: nextDraft.title,
      body: nextDraft.body,
    });
    onMutated?.();
  }

  return (
    <>
      <TaskDetailList
        activity={activity}
        attention={attention}
        comments={comments}
        detail={detail}
        disabled={connection.phase !== "connected"}
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
        onAddDependency={(direction) => {
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
          if (activeDestination?.kind === "taskDetail") {
            replaceSidebar(destination);
          } else {
            void openSidebar(destination);
          }
        }}
        onRemoveDependency={(pair) => {
          mutations.removeDependency.mutate(pair);
        }}
        onSelectDependencyTask={(taskID) => {
          if (activeDestination?.kind === "taskDetail") {
            replaceSidebar({
              kind: "taskDetail",
              taskID,
              ...(activeDestination.mode === undefined ? {} : { mode: activeDestination.mode }),
              ...(activeDestination.onMutated === undefined
                ? {}
                : { onMutated: activeDestination.onMutated }),
            });
            return;
          }
          void navigation.replaceTask(taskID);
        }}
        onNewCommentBodyChange={setNewCommentBody}
        onEditingCommentChange={setEditingComment}
        onQuestionSelectionChange={(askID, selection) => {
          setQuestionSelections((previous) => new Map(previous).set(askID, selection));
        }}
        onSaveDraft={saveDraft}
        openLink={openLink}
        questionSelections={questionSelections}
        resumeContinuation={resumeContinuation}
        selectedTab={selectedTab}
        setTab={setSelectedTab}
        updateError={update.error}
        updatePending={update.isPending}
      />
      <TaskInitiatingActionDialogs
        continuation={resumeContinuation}
        onResult={(result: TaskInitiatingActionDialogResult) => {
          if (result.kind === "continue") {
            void resumeContinuation.run(result.action, result.selection);
          }
        }}
      />
    </>
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

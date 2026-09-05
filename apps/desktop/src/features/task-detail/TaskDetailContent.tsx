import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  errorMessage,
  type QuestionAnswerInput,
  type TaskAttention,
  type TaskDependencyDirection,
  type TaskDetail,
} from "@/api";
import type {
  SidebarPageNavigator,
  SidebarMode,
  SidebarRootController,
  SidebarDestination,
  TaskDetailInitialFocus,
} from "@/app-facade";
import {
  queryKeys,
  useAppNavigation,
  useAppServices,
  useConnectionSnapshot,
  useStatusController,
} from "@/app-facade";
import { useUpdateTask } from "@/shared/task-mutations";
import {
  initialDescriptionPresentationState,
  type DescriptionPresentationState,
} from "./TaskDetailDescriptionPresentation";
import { TaskDeleteProvider } from "./TaskDeleteButton";
import { TaskDetailList } from "./TaskDetailList";
import type { TaskDetailSessionChatEntry } from "./taskDetailSessionChat";
import { taskDetailSidebarDestination } from "./taskDetailSidebarDestination";
import type { TaskDetailDeleteDismissal } from "./taskDetailDismissal";
import {
  emptyPromptAnswerState,
  promptAnswerKey,
  type PromptAnswerKey,
  type PromptAnswerState,
} from "./PromptAnswerState";
import { PromptAnswerCoordinator } from "./PromptAnswerCoordinator";
import type { PromptPrimaryFocusRequest } from "./PromptPrimaryControlRegistry";
import { promptSubmissionHandoff } from "./PromptSubmissionHandoff";
import { questionAnswerBatchInput, type QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";
import { TaskInitiatingActionProvider } from "./TaskResumeButton";
import type { TaskDraft } from "./TaskDetailRows";
import { useTaskMutations, useTaskDetailLiveRefresh } from "./useTaskDetailData";
import type { useTaskActivity, useTaskAttention, useTaskComments } from "./useTaskDetailData";

// TaskDraftState tracks the editable title/body draft alongside the server
// snapshot (`base`) the draft last synced to. Comparing the draft to `base`
// distinguishes genuine unsaved user edits from a draft that merely lags behind
// a server refresh, which lets a clean surface follow live updates while a
// dirty surface keeps the user's in-progress edits.
type TaskDraftState = Readonly<{
  base: TaskDraft;
  draft: TaskDraft;
}>;

interface PromptAttentionReconciliationIdentity {
  readonly generatedAt: number;
  readonly requestSequence: number;
}

interface PromptAttentionReconciliationRequest {
  readonly cacheAtStart: TaskAttention | undefined;
  readonly requestSequence: number;
}

interface AcceptedPromptAttentionReconciliation extends PromptAttentionReconciliationIdentity {
  readonly snapshot: TaskAttention;
}

function newPromptAttentionReconciliationOrder() {
  let nextRequestSequence = 0;
  let lastAccepted: AcceptedPromptAttentionReconciliation | undefined;
  return {
    accept(
      request: PromptAttentionReconciliationRequest,
      candidate: TaskAttention,
      current: TaskAttention | undefined,
    ): boolean {
      const olderThanAccepted =
        lastAccepted !== undefined &&
        (candidate.generatedAt < lastAccepted.generatedAt ||
          (candidate.generatedAt === lastAccepted.generatedAt &&
            request.requestSequence < lastAccepted.requestSequence));
      const unsequencedEqualTimestampWrite =
        current?.generatedAt === candidate.generatedAt &&
        current !== request.cacheAtStart &&
        current !== lastAccepted?.snapshot;
      if (
        olderThanAccepted ||
        unsequencedEqualTimestampWrite ||
        (current !== undefined && current.generatedAt > candidate.generatedAt)
      ) {
        return false;
      }
      lastAccepted = {
        generatedAt: candidate.generatedAt,
        requestSequence: request.requestSequence,
        snapshot: candidate,
      };
      return true;
    },
    beginRequest(cacheAtStart: TaskAttention | undefined): PromptAttentionReconciliationRequest {
      nextRequestSequence += 1;
      return { cacheAtStart, requestSequence: nextRequestSequence };
    },
  };
}

export function TaskDetailContent({
  activity,
  attention,
  comments,
  detail,
  initialFocus,
  onDeleteDismiss,
  onMutated,
  openSessionChat,
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
  openSessionChat?: TaskDetailSessionChatEntry | undefined;
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
  const serverDraft = taskDraft(detail);
  const [draftState, setDraftState] = useState<TaskDraftState>(() => ({
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
  const promptAnswers = useTaskPromptAnswers({ attention, detail });
  const update = useUpdateTask(detail.id, detail.projectID);
  useTaskDetailRetainedCapture({
    base: draftState.base,
    descriptionPresentation,
    draft: draftState.draft,
    editingComment,
    navigator,
    newCommentBody,
    selectedTab,
  });
  const reportActionError = useCallback(
    (action: "dependency_add" | "dependency_remove" | "interrupt", error: unknown) => {
      const notice =
        action === "interrupt"
          ? { id: "task-interrupt-error", title: t("board.interruptFailed") }
          : action === "dependency_add"
            ? {
                id: "task-dependency-add-error",
                title: t("task.dependenciesAddFailed"),
              }
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
  // Reconcile the draft with the latest server snapshot during render (the
  // React "adjust state on prop change" pattern). A clean surface follows live
  // server updates; a surface with unsaved edits keeps the user's draft so a
  // background refresh never clobbers in-progress work. A draft that has
  // caught up to the server (e.g. after a save) re-baselines so subsequent
  // server changes are followed again.
  const reconciled = reconcileDraftState(draftState, serverDraft);
  if (reconciled !== draftState) {
    setDraftState(reconciled);
  }
  const draft = reconciled.draft;
  const focusPresentation = taskDetailFocusPresentation({
    initialFocus,
    localDependencyFocusRequest,
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
          answerQuestion={promptAnswers.answerQuestion}
          attention={attention}
          comments={comments}
          detail={detail}
          disabled={connection.phase !== "connected"}
          draft={draft}
          descriptionPresentation={descriptionPresentation}
          editingComment={editingComment}
          focusRequestKey={dependencyFocusRequestKey(detail.id, localDependencyFocusRequest)}
          initialFocus={focusPresentation}
          mutations={mutations}
          newCommentBody={newCommentBody}
          relationshipNavigationAvailable={relationshipNavigationAvailable}
          onDraftChange={(nextDraft) => {
            setDraftState({ base: reconciled.base, draft: nextDraft });
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
          onQuestionSelectionChange={(key: PromptAnswerKey, selection: QuestionSelectionState) => {
            promptAnswers.setState((previous) => previous.withSelection(key, selection));
          }}
          onSaveDraft={saveDraft}
          openSessionChat={openSessionChat}
          primaryFocusRequest={promptAnswers.primaryFocusRequest}
          promptAnswerState={promptAnswers.state}
          selectedTab={selectedTab}
          setTab={setSelectedTab}
          updateError={update.error}
          updatePending={update.isPending}
        />
      </TaskDeleteProvider>
    </TaskInitiatingActionProvider>
  );
}

function useTaskPromptAnswers({
  attention,
  detail,
}: Readonly<{
  attention: ReturnType<typeof useTaskAttention>;
  detail: TaskDetail;
}>) {
  const { api } = useAppServices();
  const { push } = useStatusController();
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [state, setState] = useState<PromptAnswerState>(emptyPromptAnswerState);
  const [primaryFocusRequest, setPrimaryFocusRequest] = useState<PromptPrimaryFocusRequest | undefined>(
    undefined,
  );
  const requestSequence = useMemo(() => ({ value: 0 }), [detail.id]);
  const taskScope = useMemo(() => ({ mounted: true }), [detail.id]);
  useEffect(
    () => () => {
      taskScope.mounted = false;
    },
    [taskScope],
  );
  const coordinator = useMemo(() => {
    const reconciliationOrder = newPromptAttentionReconciliationOrder();
    return new PromptAnswerCoordinator({
      invalidateAttention: async () => {
        await queryClient.cancelQueries({
          exact: true,
          queryKey: queryKeys.taskAttention(detail.id),
        });
        await queryClient.invalidateQueries({
          exact: true,
          queryKey: queryKeys.taskAttention(detail.id),
          refetchType: "none",
        });
      },
      isMounted: () => taskScope.mounted,
      notifyFailure: (failure) => {
        push({
          body: `${failure.taskShortID} · ${failure.taskTitle}\n${errorMessage(failure.cause)}`,
          durationMs: Infinity,
          id: [
            "task-prompt-answer",
            failure.kind,
            failure.taskID,
            failure.promptKey.sessionID,
            failure.promptKey.stepID,
            failure.promptKey.toolCallID,
          ].join(":"),
          title: t("states.error"),
          tone: "danger",
        });
      },
      readAttention: async () => {
        const attentionKey = queryKeys.taskAttention(detail.id);
        const request = reconciliationOrder.beginRequest(
          queryClient.getQueryData<TaskAttention>(attentionKey),
        );
        const fresh = await api.listTaskAttention(detail.id);
        const accepted = queryClient.setQueryData<TaskAttention>(attentionKey, (current) =>
          reconciliationOrder.accept(request, fresh, current) ? fresh : current,
        );
        if (accepted === undefined) {
          throw new Error("accepted Task attention reconciliation snapshot is unavailable");
        }
        return accepted.items.filter(
          (item): item is Extract<(typeof accepted.items)[number], { kind: "question" }> =>
            item.kind === "question",
        );
      },
      task: { id: detail.id, shortID: detail.shortID, title: detail.title },
      updateState: setState,
    });
  }, [api, detail.id, detail.shortID, detail.title, push, queryClient, t, taskScope]);
  const answerQuestion = useMemo<QuestionAnswerMutation>(
    () => ({
      isPending: false,
      async mutateAsync(
        input: QuestionAnswerInput,
        attempt: Parameters<QuestionAnswerMutation["mutateAsync"]>[1],
      ): Promise<void> {
        requestSequence.value += 1;
        const handoff = promptSubmissionHandoff({
          attentionItems: (attention.data?.items ?? []).filter(
            (item) => item.kind !== "question" || !state.isMasked(promptAnswerKey(item)),
          ),
          requestID: requestSequence.value,
          submittedKey: promptAnswerKey(attempt.attention),
        });
        setPrimaryFocusRequest(handoff.primaryFocusRequest);
        await coordinator.submit({
          attention: attempt.attention,
          selection: attempt.selection,
          send: async () => api.answerPromptBatch(questionAnswerBatchInput(input)),
        });
      },
    }),
    [api, attention.data?.items, coordinator, requestSequence, state],
  );
  const projectedState =
    attention.data === undefined
      ? state
      : state.reconcileProjection(
          attention.data.items.filter(
            (item): item is Extract<(typeof attention.data.items)[number], { kind: "question" }> =>
              item.kind === "question",
          ),
        );
  if (projectedState !== state) {
    setState(projectedState);
  }
  return {
    answerQuestion,
    primaryFocusRequest,
    reset(): void {
      setState(emptyPromptAnswerState());
      setPrimaryFocusRequest(undefined);
    },
    setState,
    state: projectedState,
  } as const;
}

function taskDetailFocusPresentation({
  initialFocus,
  localDependencyFocusRequest,
}: Readonly<{
  initialFocus: TaskDetailInitialFocus | undefined;
  localDependencyFocusRequest: number | null;
}>): TaskDetailInitialFocus | undefined {
  if (localDependencyFocusRequest !== null) {
    return { kind: "dependencies" };
  }
  return initialFocus;
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
    initialPreparedDependency: {
      direction: direction === "blocked-by" ? ("blocks" as const) : ("blocked-by" as const),
      taskID: detail.id,
      shortID: detail.shortID,
      title: detail.title,
      workflowID: detail.workflowID,
      status: detail.status,
    },
    kind: "newTask" as const,
    mode: "overlay" as const,
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
  selectedTab,
}: Readonly<{
  base: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  draft: TaskDraft;
  editingComment: Readonly<{ id: string; body: string }> | null;
  navigator?: SidebarPageNavigator | undefined;
  newCommentBody: string;
  selectedTab: "comments" | "activity";
}>) {
  useEffect(() => {
    if (navigator === undefined) return;
    return navigator.registerCapture(() => ({
      base,
      descriptionPresentation,
      draft,
      editingComment,
      newCommentBody,
      selectedTab,
    }));
  }, [base, descriptionPresentation, draft, editingComment, navigator, newCommentBody, selectedTab]);
}

type TaskDetailRetainedState = Readonly<{
  base: TaskDraft;
  descriptionPresentation: DescriptionPresentationState;
  draft: TaskDraft;
  editingComment: Readonly<{ id: string; body: string }> | null;
  newCommentBody: string;
  selectedTab: "comments" | "activity";
}>;

const taskDetailRetainedStateSchema = z.object({
  base: z.object({ body: z.string(), title: z.string() }),
  descriptionPresentation: z.object({ editing: z.boolean(), expanded: z.boolean() }),
  draft: z.object({ body: z.string(), title: z.string() }),
  editingComment: z.object({ body: z.string(), id: z.string() }).nullable(),
  newCommentBody: z.string(),
  selectedTab: z.enum(["comments", "activity"]),
});

function decodeTaskDetailRetainedState(state: unknown): TaskDetailRetainedState | undefined {
  return taskDetailRetainedStateSchema.safeParse(state).data;
}

function dependencyFocusRequestKey(taskID: string, request: number | null): string | undefined {
  return request === null ? undefined : `${taskID}:dependencies:${request.toString()}`;
}

function taskDraft(detail: TaskDetail): TaskDraft {
  return { title: detail.title, body: detail.body };
}

function sameDraft(a: TaskDraft, b: TaskDraft): boolean {
  return a.title === b.title && a.body === b.body;
}

function reconcileDraftState(state: TaskDraftState, serverDraft: TaskDraft): TaskDraftState {
  const hasUnsavedEdits = !sameDraft(state.draft, state.base);
  if (!hasUnsavedEdits) {
    // Clean surface: track the latest server values (re-baseline on change).
    return sameDraft(state.base, serverDraft) ? state : { base: serverDraft, draft: serverDraft };
  }
  if (sameDraft(state.draft, serverDraft)) {
    // The draft caught up to the server (e.g. the edit was just saved): treat
    // it as clean again so future server changes are followed.
    return { base: serverDraft, draft: serverDraft };
  }
  // Unsaved edits diverge from the server: keep them; edits take priority.
  return state;
}

import type { DragEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  hasSelectedWorkflow,
  type BoardColumn,
  type SelectedWorkflowBoard,
} from "@/api";
import { errorMessage } from "@/api";
import { useAppNavigation } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { useSidebar } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { useWindowChromeTitle } from "@/app-facade";
import {
  TaskInitiatingActionDialogs,
  startTaskInitiatingAction,
  type TaskInitiatingActionDialogResult,
} from "@/shared/execution-target";
import { ProjectLabelsProvider, useProjectLabelFilter } from "@/shared/labels";
import { WorkflowValidationIssues } from "@/shared/workflow-validation";
import { ErrorState, FloatingNoticeIsland, LoadingState } from "@/ui";
import { BoardHoverMenu } from "./BoardHoverMenu";
import { BoardHorizontalScrollbar } from "./BoardHorizontalScrollbar";
import { useBoardDragAutoScroll } from "./BoardDragAutoScroll";
import { BoardRailMotionController } from "./BoardRailMotionController";
import { TaskDeleteConfirmationFallbackDialog } from "./TaskDeleteConfirmation";
import { taskDeleteWindowOptions, type TaskDeleteTarget } from "./taskDeleteConfirmationModel";
import type { BoardColumnDropState } from "./BoardDragTypes";
import type { ActiveBoardCardDrag } from "./BoardDragState";
import { BoardBackgroundRefreshNotice } from "./BoardBackgroundRefreshNotice";
import { BoardNoWorkflowState } from "./BoardNoWorkflowState";
import { classifyDrop } from "./BoardDropActions";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";
import { ManualMoveDialog } from "./ManualMoveDialog";
import { useBoardInitiatingActionController } from "./useBoardInitiatingActionController";
import { taskDetailRouteShouldClose } from "./taskDetailRouteLifecycle";
import { useManualMoveController } from "./useManualMoveController";
import "./board.css";
import { BoardFilterGenerationProvider } from "./BoardFilterGenerationContext";
import { BoardLabelFilterChrome, BoardMembershipRefreshBinding } from "./BoardLabelFilter";
import { ignoreBoardMembershipRefresh, type BoardMembershipRefreshRef } from "./BoardMembershipRefresh";
import { useBoard, useBoardTaskActions, useProjectBoardSubscription } from "./useBoardData";
import { useBoardLoadErrorReporter } from "./useBoardLoadErrorReporter";

export type BoardRouteProps = Readonly<{
  projectId: string;
  workflowId: string | undefined;
  selectedTaskId: string;
}>;

const emptyExpandedEmptyColumnIDs: ReadonlySet<string> = new Set();

const manualMoveBlockerTranslationKeys = {
  invalid_workflow: "board.moveBlockedInvalidWorkflow",
  no_source_position: "board.moveBlockedNoSource",
  unsupported_destination: "board.moveBlockedUnsupportedDestination",
  waiting_question: "board.moveBlockedWaitingQuestion",
  lifecycle_conflict: "board.moveBlockedLifecycle",
  context_session_unavailable: "board.moveBlockedContextSession",
  no_usable_transition: "board.moveBlockedNoUsableTransition",
  parallel_branch_requires_fan_out: "board.moveBlockedFanOut",
} as const;

function manualMoveBlockerCopy(reason: string, translate: (key: string) => string): string {
  const key =
    Object.entries(manualMoveBlockerTranslationKeys).find(([candidate]) => candidate === reason)?.[1] ??
    "board.moveBlockedGeneric";
  return translate(key);
}

export function BoardRoute({ projectId, workflowId, selectedTaskId }: BoardRouteProps) {
  const reportBoardLoadError = useBoardLoadErrorReporter();
  const membershipRefreshRef = useRef<BoardMembershipRefreshRef["current"]>(ignoreBoardMembershipRefresh);
  return (
    <ProjectLabelsProvider
      onBackgroundError={reportBoardLoadError}
      onMembershipRefresh={async (effect) => membershipRefreshRef.current(effect)}
      projectID={projectId}
      subscribeToProject={false}
    >
      <BoardRouteWithLabels
        membershipRefreshRef={membershipRefreshRef}
        onBackgroundError={reportBoardLoadError}
        projectId={projectId}
        selectedTaskId={selectedTaskId}
        workflowId={workflowId}
      />
    </ProjectLabelsProvider>
  );
}

function BoardRouteWithLabels({
  membershipRefreshRef,
  onBackgroundError,
  projectId,
  workflowId,
  selectedTaskId,
}: BoardRouteProps &
  Readonly<{
    membershipRefreshRef: BoardMembershipRefreshRef;
    onBackgroundError(error: unknown): void;
  }>) {
  const filter = useProjectLabelFilter();
  return (
    <BoardFilterGenerationProvider
      desiredFilter={filter.state.filter}
      initialFilter={filter.state.filter}
      onBackgroundError={onBackgroundError}
      queriesEnabled={filter.persistence.status !== "loading"}
    >
      <BoardMembershipRefreshBinding membershipRefreshRef={membershipRefreshRef} />
      <BoardRouteData
        onBackgroundError={onBackgroundError}
        projectId={projectId}
        selectedTaskId={selectedTaskId}
        workflowId={workflowId}
      />
    </BoardFilterGenerationProvider>
  );
}

function BoardRouteData({
  onBackgroundError: reportBoardLoadError,
  projectId,
  workflowId,
  selectedTaskId,
}: BoardRouteProps &
  Readonly<{
    onBackgroundError(error: unknown): void;
  }>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const navigation = useAppNavigation();
  const { activeDestination, closeSidebar } = useSidebar();
  const reportBoardNavigationError = useCallback(
    (error: unknown) => {
      push({
        id: "board-navigation-error",
        tone: "danger",
        title: t("board.navigationFailed"),
        body: errorMessage(error),
        durationMs: Infinity,
      });
    },
    [push, t],
  );
  const boardQuery = useBoard(projectId, workflowId);
  const board = boardQuery.data;
  const selectedWorkflowID = board?.selectedWorkflow?.id;
  const handleSelectedTaskDeleted = useCallback(() => {
    // The task detail sidebar is opened independently of the route, so closing
    // the route task alone would leave it mounted and refetching the now-deleted
    // task into an error state. Close it too when it targets the deleted task.
    if (activeDestination?.kind === "taskDetail" && activeDestination.taskID === selectedTaskId) {
      closeSidebar();
    }
    void navigation.closeProjectTask(projectId, workflowId).catch(reportBoardNavigationError);
  }, [
    activeDestination,
    closeSidebar,
    navigation,
    projectId,
    reportBoardNavigationError,
    selectedTaskId,
    workflowId,
  ]);
  useProjectBoardSubscription(projectId, workflowId, {
    onBackgroundError: reportBoardLoadError,
    onSelectedTaskDeleted: handleSelectedTaskDeleted,
    selectedTaskID: selectedTaskId,
    selectedWorkflowID,
  });

  if (boardQuery.isPending && board === undefined) {
    return <LoadingState chromePadding reveal={false} title={t("states.loading")} />;
  }
  if (boardQuery.isError && board === undefined) {
    return (
      <ErrorState
        body={errorMessage(boardQuery.error)}
        chromePadding
        onRetry={() => void boardQuery.refetch().catch(reportBoardLoadError)}
        reveal={false}
        retryLabel={t("app.retry")}
        title={t("states.error")}
      />
    );
  }
  if (board === undefined || !hasSelectedWorkflow(board)) {
    return <BoardNoWorkflowState projectID={projectId} />;
  }

  return (
    <BoardContent
      board={board}
      boardQueryWorkflowID={workflowId}
      boardRefreshError={boardQuery.isError ? boardQuery.error : null}
      onBoardRefreshRetry={() => {
        void boardQuery.refetch().catch(reportBoardLoadError);
      }}
      selectedTaskId={selectedTaskId}
    />
  );
}

function BoardContent({
  board,
  boardQueryWorkflowID,
  boardRefreshError,
  onBoardRefreshRetry,
  selectedTaskId,
}: Readonly<{
  board: SelectedWorkflowBoard;
  boardQueryWorkflowID: string | undefined;
  boardRefreshError: Error | null;
  onBoardRefreshRetry(): void;
  selectedTaskId: string;
}>) {
  const { t } = useTranslation();
  const [workflowIssuesCollapsed, setWorkflowIssuesCollapsed] = useState(false);
  const [activeDrag, setActiveDrag] = useState<ActiveBoardCardDrag | null>(null);
  const [pendingCardMove, setPendingCardMove] = useState<PendingBoardCardMove | null>(null);
  const [expandedEmptyColumns, setExpandedEmptyColumns] = useState<
    Readonly<{ ids: ReadonlySet<string>; scope: string }>
  >(() => ({ ids: new Set(), scope: "" }));
  const { push } = useStatusController();
  const { api, nativeBridge } = useAppServices();
  const navigation = useAppNavigation();
  const scrollportRef = useRef<HTMLDivElement | null>(null);
  const dragAutoScroll = useBoardDragAutoScroll({ active: activeDrag !== null, rootRef: scrollportRef });
  const stopDragAutoScroll = dragAutoScroll.stop;
  const cancelActiveDrag = useCallback(() => {
    stopDragAutoScroll();
    setActiveDrag(null);
  }, [stopDragAutoScroll]);
  const { activeDestination, openSidebar, replaceSidebar } = useSidebar();
  const connection = useConnectionSnapshot();
  const actions = useBoardTaskActions();
  const reportActionError = useCallback(
    (id: string, title: string, error: unknown) => {
      const body = errorMessage(error);
      push({ id, tone: "danger", title, body, durationMs: Infinity });
    },
    [push],
  );
  const reportStartError = useCallback(
    (error: unknown) => {
      reportActionError("board-start-error", t("board.startFailed"), error);
    },
    [reportActionError, t],
  );
  const reportMoveError = useCallback(
    (error: unknown) => {
      reportActionError("board-move-error", t("board.moveFailed"), error);
    },
    [reportActionError, t],
  );
  const reportMovePreviewBlocked = useCallback(
    (reason: string) => {
      push({
        id: "board-move-preview-blocked",
        tone: "warning",
        title: t("board.moveBlocked"),
        body: manualMoveBlockerCopy(reason, t),
        durationMs: Infinity,
      });
    },
    [push, t],
  );
  const {
    actionsDisabled: initiatingActionsDisabled,
    initiatingAction,
    runCardAction,
  } = useBoardInitiatingActionController({
    api,
    connected: connection.phase === "connected",
    moveErrorTitle: t("board.moveFailed"),
    onActionError: reportActionError,
    onApplied: actions.refresh,
    onPendingMoveChange: setPendingCardMove,
    refreshErrorTitle: t("board.loadFailed"),
    startErrorTitle: t("board.startFailed"),
  });
  const manualMove = useManualMoveController({
    api,
    onPreviewBlocked: reportMovePreviewBlocked,
    onPreviewError: reportMoveError,
    runAction: runCardAction,
  });
  const actionsDisabled = initiatingActionsDisabled || manualMove.actionsDisabled;
  const taskDeleteDialog = useNativeDialogFallback<TaskDeleteTarget>({
    errorNoticeID: "task-delete-window-error",
    errorTitle: t("board.deleteTaskWindowError"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (target) => {
      await nativeBridge.dialogs.openWindow(taskDeleteWindowOptions(target, t("board.deleteTaskTitle")));
    },
    renderFallback: (target, close) => (
      <TaskDeleteConfirmationFallbackDialog
        disabled={actions.delete.isPending}
        onClose={close}
        onConfirm={() => {
          void confirmDeleteTask(target, close);
        }}
      />
    ),
  });

  const activeColumns = useMemo(
    () => board.columns.filter((column) => !column.isBacklog && !column.isDone),
    [board.columns],
  );
  const firstActive = activeColumns[0];
  const columnExpansionScope = `${board.projectID}:${board.selectedWorkflow.id}`;
  const expandedEmptyColumnIDs =
    expandedEmptyColumns.scope === columnExpansionScope
      ? expandedEmptyColumns.ids
      : emptyExpandedEmptyColumnIDs;
  useWindowChromeTitle(board.selectedWorkflow.name || board.projectName);
  const reportCardsLoadError = useCallback(
    (error: unknown) => {
      reportActionError("board-cards-load-error", t("board.cardsLoadFailed"), error);
    },
    [reportActionError, t],
  );
  const reportNavigationError = useCallback(
    (error: unknown) => {
      reportActionError("board-navigation-error", t("board.navigationFailed"), error);
    },
    [reportActionError, t],
  );

  useEffect(() => {
    if (selectedTaskId.length === 0) {
      return;
    }
    let active = true;
    void openSidebar({
      kind: "taskDetail",
      mode: "overlay",
      onMutated: undefined,
      taskID: selectedTaskId,
    }).then((result) => {
      if (active && taskDetailRouteShouldClose(result)) {
        void navigation
          .closeProjectTask(board.projectID, board.selectedWorkflow.id)
          .catch(reportNavigationError);
      }
    });
    return () => {
      active = false;
    };
  }, [
    board.projectID,
    board.selectedWorkflow.id,
    navigation,
    openSidebar,
    reportNavigationError,
    selectedTaskId,
  ]);

  useEffect(() => {
    const handleDocumentDrop = (event: Event): void => {
      const root = scrollportRef.current;
      if (root !== null && event.target instanceof Node && root.contains(event.target)) {
        stopDragAutoScroll();
        return;
      }
      cancelActiveDrag();
    };
    const handleCancellation = (): void => {
      cancelActiveDrag();
    };
    document.addEventListener("drop", handleDocumentDrop, true);
    document.addEventListener("dragend", handleCancellation, true);
    return () => {
      document.removeEventListener("drop", handleDocumentDrop, true);
      document.removeEventListener("dragend", handleCancellation, true);
    };
  }, [cancelActiveDrag, stopDragAutoScroll]);

  function dropTask(event: DragEvent<HTMLElement>, column: BoardColumn): void {
    event.preventDefault();
    const dragPayload = activeDrag === null ? null : activeDrag.payload;
    cancelActiveDrag();
    if (dragPayload === null || actionsDisabled) {
      reportRejectedDrop();
      return;
    }
    const dropAction = classifyDrop(column, dragPayload, firstActive?.id);
    if (dropAction.kind === "start") {
      const pendingMove = { taskID: dragPayload.taskID, targetColumnID: column.id };
      runCardAction(startTaskInitiatingAction(dragPayload.taskID), pendingMove);
      return;
    }
    if (dropAction.kind === "no_op") {
      return;
    }
    if (dropAction.kind === "move") {
      manualMove.preview(dragPayload.taskID, column.id);
      return;
    }
    reportRejectedDrop();
  }

  function interruptTask(taskID: string): void {
    void actions.interrupt.execute(taskID).catch(reportInterruptError);
  }

  function resumeTask(taskID: string): void {
    void actions.resume.execute(taskID).catch(reportResumeError);
  }

  function deleteTask(taskID: string): void {
    void taskDeleteDialog.open({ taskID });
  }

  async function confirmDeleteTask(target: TaskDeleteTarget, close: () => void): Promise<void> {
    try {
      await actions.delete.mutateAsync(target.taskID);
      if (target.taskID === selectedTaskId) {
        await navigation
          .closeProjectTask(board.projectID, board.selectedWorkflow.id)
          .catch(reportNavigationError);
      }
      close();
    } catch (error) {
      reportDeleteError(error);
    }
  }

  function reportInterruptError(error: unknown): void {
    reportActionError("board-interrupt-error", t("board.interruptFailed"), error);
  }

  function reportResumeError(error: unknown): void {
    reportActionError("board-resume-error", t("board.resumeFailed"), error);
  }

  function reportDeleteError(error: unknown): void {
    reportActionError("board-delete-error", t("board.deleteFailed"), error);
  }

  function reportRejectedDrop(): void {
    push({
      id: "board-drop-rejected",
      tone: "warning",
      title: t("board.dropRejected"),
      body: t("board.dropRejectedBody"),
    });
  }

  function columnDropState(column: BoardColumn): BoardColumnDropState {
    if (activeDrag === null) {
      return "idle";
    }
    if (actionsDisabled) {
      return "blocked";
    }
    const action = classifyDrop(column, activeDrag.payload, firstActive?.id);
    return action.kind === "reject" ? "blocked" : "idle";
  }

  function columnIsCollapsed(column: BoardColumn): boolean {
    return (
      !column.isBacklog &&
      column.id !== firstActive?.id &&
      column.taskCount === 0 &&
      !expandedEmptyColumnIDs.has(column.id)
    );
  }

  function expandColumn(columnID: string): void {
    setExpandedEmptyColumns((current) => {
      const next = new Set(current.scope === columnExpansionScope ? current.ids : []);
      next.add(columnID);
      return { ids: next, scope: columnExpansionScope };
    });
  }


  function handleTaskInitiatingDialogResult(result: TaskInitiatingActionDialogResult): void {
    if (result.kind === "view_dependencies") {
      openTaskDependencies(result.taskID);
      return;
    }
    const targetColumnID = result.action.kind === "move" ? result.action.input.targetNodeID : firstActive?.id;
    if (targetColumnID === undefined) {
      reportStartError(
        new Error(
          `Cannot continue ${result.action.kind} action for Task ${result.action.kind === "start" ? result.action.taskID : result.action.input.taskID}: the Workflow has no target board column.`,
        ),
      );
      return;
    }
    runCardAction(
      result.action,
      {
        taskID: result.action.kind === "start" ? result.action.taskID : result.action.input.taskID,
        targetColumnID,
      },
      result.selection,
    );
  }

  function openTask(taskID: string): void {
    void navigation
      .openProjectTask(board.projectID, board.selectedWorkflow.id, taskID)
      .catch(reportNavigationError);
  }

  function openTaskDependencies(taskID: string): void {
    const destination = {
      kind: "taskDetail" as const,
      initialFocus: { kind: "dependencies" as const },
      mode: "overlay" as const,
      taskID,
    };
    if (activeDestination?.kind === "taskDetail") {
      replaceSidebar(destination);
      return;
    }
    void openSidebar(destination);
  }

  function selectWorkflow(workflowID: string): void {
    void navigation.openProject(board.projectID, workflowID).catch(reportNavigationError);
  }

  function editWorkflow(workflowID: string): void {
    void navigation
      .openWorkflowEditor({ projectID: board.projectID, workflowID })
      .catch(reportNavigationError);
  }

  function openNewTask(): void {
    void openSidebar({
      boardQueryWorkflowID,
      kind: "newTask",
      mode: "overlay",
      projectID: board.projectID,
      workflowID: board.selectedWorkflow.id,
    });
  }

  function openLinkWorkflow(): void {
    void openSidebar({
      kind: "linkWorkflow",
      mode: "overlay",
      projectID: board.projectID,
      selectedWorkflowID: board.selectedWorkflow.id,
    });
  }

  return (
    <div className="relative flex h-full min-h-0 min-w-0 w-full flex-col">
      <BoardLabelFilterChrome />
      <div className="relative min-h-0 min-w-0 flex-1">
        <div
          className="h-full min-h-0 min-w-0 w-full overflow-x-auto hide-scrollbar"
          data-testid="board-scrollport"
          onDragLeave={dragAutoScroll.onBoardDragLeave}
          onDragOver={dragAutoScroll.onBoardDragOver}
          ref={scrollportRef}
          role="list"
        >
          <BoardRailMotionController
            activeDrag={activeDrag}
            actionsDisabled={actionsDisabled}
            board={board}
            columnDropState={columnDropState}
            columnIsCollapsed={columnIsCollapsed}
            firstActiveID={firstActive?.id}
            onCardClick={openTask}
            onCardDragEnd={cancelActiveDrag}
            onCardDragStart={(drag) => {
              setActiveDrag(drag);
            }}
            onCardsLoadError={reportCardsLoadError}
            onDeleteTask={deleteTask}
            onDropTask={dropTask}
            onExpandColumn={expandColumn}
            onInterruptTask={interruptTask}
            onRegisterColumnScrollport={dragAutoScroll.registerColumnScrollport}
            pendingCardMove={pendingCardMove}
            onResumeTask={resumeTask}
            pendingInterruptTaskIDs={actions.interrupt.pendingTaskIDs}
            pendingResumeTaskIDs={actions.resume.pendingTaskIDs}
            scrollportRef={scrollportRef}
          />
        </div>
        <BoardHorizontalScrollbar scrollportRef={scrollportRef} />
      </div>
      <ManualMoveDialog
        key={manualMove.pending?.id ?? "closed"}
        onCancel={manualMove.cancel}
        onSubmit={manualMove.submit}
        preview={manualMove.pending?.preview ?? null}
      />
      <TaskInitiatingActionDialogs
        continuation={initiatingAction}
        onResult={handleTaskInitiatingDialogResult}
      />
      {taskDeleteDialog.fallback}
      {boardRefreshError === null ? null : (
        <BoardBackgroundRefreshNotice error={boardRefreshError} onRetry={onBoardRefreshRetry} />
      )}
      {!board.selectedWorkflow.validForTaskCreation ? (
        <FloatingNoticeIsland
          collapsed={workflowIssuesCollapsed}
          collapseLabel={t("app.collapse")}
          expandLabel={t("app.expand")}
          onCollapsedChange={setWorkflowIssuesCollapsed}
          positionClassName="right-[var(--space-4)] bottom-[var(--space-4)]"
          title={t("board.workflowIssues")}
          tone="danger"
        >
          <WorkflowValidationIssues errors={board.selectedWorkflow.validationErrors} />
        </FloatingNoticeIsland>
      ) : null}
      <BoardHoverMenu
        board={board}
        canCreateTask={connection.phase === "connected"}
        onNewTask={openNewTask}
        onWorkflowEdit={editWorkflow}
        onWorkflowLink={openLinkWorkflow}
        onWorkflowSelect={selectWorkflow}
      />
    </div>
  );
}

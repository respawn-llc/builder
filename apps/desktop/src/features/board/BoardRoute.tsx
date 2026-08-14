import type { DragEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { hasSelectedWorkflow, type BoardColumn, type SelectedWorkflowBoard } from "@/api";
import { errorMessage } from "@/api";
import { useAppNavigation } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { SidebarRootOwner, useOwnedSidebarRoots } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { reportNonCancelledError, useStatusController } from "@/app-facade";
import { useWindowChromeTitle } from "@/app-facade";
import {
  TaskInitiatingActionDialogs,
  startTaskInitiatingAction,
  type TaskInitiatingActionDialogResult,
} from "@/shared/execution-target";
import { ProjectLabelsProvider, useProjectLabelFilter } from "@/shared/labels";
import { TaskDeleteConfirmationDialog } from "@/shared/task-delete";
import { WorkflowValidationIssues } from "@/shared/workflow-validation";
import { ErrorState, FloatingNoticeIsland, LoadingState } from "@/ui";
import { BoardHoverMenu } from "./BoardHoverMenu";
import { BoardHorizontalScrollbar } from "./BoardHorizontalScrollbar";
import { BoardRailMotionController } from "./BoardRailMotionController";
import { taskDeleteWindowOptions, type TaskDeleteTarget } from "./taskDeleteConfirmationModel";
import type { BoardColumnDropState } from "./BoardDragTypes";
import { classifyBoardColumnDropState, useBoardDragLifecycle } from "./BoardDragState";
import { BoardBackgroundRefreshNotice } from "./BoardBackgroundRefreshNotice";
import { BoardNoWorkflowState } from "./BoardNoWorkflowState";
import { classifyDrop } from "./BoardDropActions";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";
import { ManualMoveDialog } from "./ManualMoveDialog";
import { useBoardInitiatingActionController } from "./useBoardInitiatingActionController";
import { useBoardResumeAction } from "./useBoardResumeAction";
import { useManualMoveController } from "./useManualMoveController";
import "./board.css";
import { BoardFilterRow } from "./BoardFilterRow";
import { BoardQueryProvider } from "./BoardQueryContext";
import { completeBoardWorkflowLink } from "./boardWorkflowLinkCompletion";
import { useBoard, useBoardTaskActions, useProjectBoardSubscription } from "./useBoardData";
import { useBoardLoadErrorReporter } from "./useBoardLoadErrorReporter";

export type BoardRouteProps = Readonly<{
  projectId: string;
  workflowId: string | undefined;
  selectedTaskId: string;
}>;

const emptyExpandedEmptyColumnIDs: ReadonlySet<string> = new Set();

function boardDragDisabled(
  actionsDisabled: boolean,
  initiatingActionRunning: boolean,
  workflowValidForTaskCreation: boolean,
): boolean {
  return actionsDisabled || initiatingActionRunning || !workflowValidForTaskCreation;
}

const manualMoveBlockerTranslationKeys = {
  invalid_workflow: "board.moveBlockedInvalidWorkflow",
  no_source_position: "board.moveBlockedNoSource",
  unsupported_destination: "board.moveBlockedUnsupportedDestination",
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
  return (
    <SidebarRootOwner>
      <ProjectLabelsProvider
        onBackgroundError={reportBoardLoadError}
        projectID={projectId}
        subscribeToProject={false}
      >
        <BoardRouteWithLabels
          onBackgroundError={reportBoardLoadError}
          projectId={projectId}
          selectedTaskId={selectedTaskId}
          workflowId={workflowId}
        />
      </ProjectLabelsProvider>
    </SidebarRootOwner>
  );
}

function BoardRouteWithLabels({
  onBackgroundError,
  projectId,
  workflowId,
  selectedTaskId,
}: BoardRouteProps &
  Readonly<{
    onBackgroundError(error: unknown): void;
  }>) {
  const filter = useProjectLabelFilter();
  return (
    <BoardQueryProvider
      key={`${projectId}:${workflowId ?? "default"}`}
      labelFilter={filter.state.filter}
      queriesEnabled={filter.persistence.status !== "loading"}
    >
      <BoardRouteData
        onBackgroundError={onBackgroundError}
        projectId={projectId}
        selectedTaskId={selectedTaskId}
        workflowId={workflowId}
      />
    </BoardQueryProvider>
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
    void navigation.closeProjectTask(projectId, workflowId).catch(reportBoardNavigationError);
  }, [navigation, projectId, reportBoardNavigationError, workflowId]);
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
  const [pendingCardMove, setPendingCardMove] = useState<PendingBoardCardMove | null>(null);
  const [expandedEmptyColumns, setExpandedEmptyColumns] = useState<
    Readonly<{ ids: ReadonlySet<string>; scope: string }>
  >(() => ({ ids: new Set(), scope: "" }));
  const { push } = useStatusController();
  const { api, nativeBridge } = useAppServices();
  const navigation = useAppNavigation();
  const scrollportRef = useRef<HTMLDivElement | null>(null);
  const { open } = useOwnedSidebarRoots();
  const connection = useConnectionSnapshot();
  const actions = useBoardTaskActions(board.projectID);
  const reportActionError = useCallback(
    (id: string, title: string, error: unknown) => {
      reportNonCancelledError(error, (failure) => {
        const body = errorMessage(failure);
        push({ id, tone: "danger", title, body, durationMs: Infinity });
      });
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
  const { initiatingAction, runCardAction } = useBoardInitiatingActionController({
    api,
    connected: connection.phase === "connected",
    moveErrorTitle: t("board.moveFailed"),
    onActionError: reportActionError,
    onApplied: actions.refresh,
    onPendingMoveChange: setPendingCardMove,
    refreshErrorTitle: t("board.loadFailed"),
    startErrorTitle: t("board.startFailed"),
  });
  const resumeAction = useBoardResumeAction(initiatingAction);
  const manualMove = useManualMoveController({
    api,
    onPreviewBlocked: reportMovePreviewBlocked,
    onPreviewError: reportMoveError,
    runAction: runCardAction,
  });
  const actionsDisabled =
    connection.phase !== "connected" || initiatingAction.pending !== null || manualMove.actionsDisabled;
  const dragDisabled = boardDragDisabled(
    actionsDisabled,
    initiatingAction.running,
    board.selectedWorkflow.validForTaskCreation,
  );
  const {
    activeDrag,
    autoScroll: dragAutoScroll,
    cancel: cancelActiveDrag,
    dragBlocked,
    start: startActiveDrag,
  } = useBoardDragLifecycle({ disabled: dragDisabled, rootRef: scrollportRef });
  const stopDragAutoScroll = dragAutoScroll.stop;
  const taskDeleteDialog = useNativeDialogFallback<TaskDeleteTarget>({
    errorNoticeID: "task-delete-window-error",
    errorTitle: t("board.deleteTaskWindowError"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (target) => {
      await nativeBridge.dialogs.openWindow(taskDeleteWindowOptions(target, t("board.deleteTaskTitle")));
    },
    renderFallback: (target, close) => (
      <TaskDeleteConfirmationDialog
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
    const root = open({
      kind: "taskDetail",
      mode: "overlay",
      onMutated: undefined,
      taskID: selectedTaskId,
    });
    void root.lifecycle.then((outcome) => {
      if (active && outcome === "closed") {
        void navigation
          .closeProjectTask(board.projectID, board.selectedWorkflow.id)
          .catch(reportNavigationError);
      }
    });
    return () => {
      active = false;
      root.release();
    };
  }, [board.projectID, board.selectedWorkflow.id, navigation, open, reportNavigationError, selectedTaskId]);

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
    if (dragPayload === null) {
      reportRejectedDrop();
      return;
    }
    const dropAction = classifyDrop(column, dragPayload, firstActive?.id);
    if (dropAction.kind === "start") {
      const pendingMove = { taskID: dragPayload.taskID, targetColumnID: column.id };
      runCardAction(startTaskInitiatingAction(dragPayload.taskID), pendingMove);
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
    void resumeAction.execute(taskID).catch(reportResumeError);
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
    return classifyBoardColumnDropState({
      column,
      drag: activeDrag,
      dragBlocked,
      firstActiveID: firstActive?.id,
    });
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
    if (result.action.kind === "resume") {
      const resumed =
        result.selection === undefined
          ? resumeAction.execute(result.action.taskID)
          : resumeAction.continueExecution(result.action, result.selection);
      void resumed.catch(reportResumeError);
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
    open(destination);
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
    open({
      boardQueryWorkflowID,
      kind: "newTask",
      mode: "overlay",
      projectID: board.projectID,
      workflowID: board.selectedWorkflow.id,
    });
  }

  function openLinkWorkflow(): void {
    open({
      kind: "linkWorkflow",
      mode: "overlay",
      onCompleted: async (completion) => {
        await completeBoardWorkflowLink(navigation, board.projectID, completion);
      },
      projectID: board.projectID,
      selectedWorkflowID: board.selectedWorkflow.id,
    });
  }

  return (
    <div className="relative flex h-full min-h-0 min-w-0 w-full flex-col">
      <div className="flex shrink-0 items-center gap-[var(--space-2)] px-[var(--space-2)] pt-[var(--space-2)]">
        <BoardFilterRow onOpenTask={openTask} projectID={board.projectID} />
      </div>
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
            dragDisabled={dragDisabled}
            firstActiveID={firstActive?.id}
            onCardClick={openTask}
            onCardDragEnd={cancelActiveDrag}
            onCardDragStart={startActiveDrag}
            onDeleteTask={deleteTask}
            onDropTask={dropTask}
            onExpandColumn={expandColumn}
            onInterruptTask={interruptTask}
            onRegisterColumnScrollport={dragAutoScroll.registerColumnScrollport}
            pendingCardMove={pendingCardMove}
            onResumeTask={resumeTask}
            pendingInterruptTaskIDs={actions.interrupt.pendingTaskIDs}
            pendingResumeTaskIDs={resumeAction.pendingTaskIDs}
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
      <BoardWorkflowIssuesNotice workflow={board.selectedWorkflow} />
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

function BoardWorkflowIssuesNotice({
  workflow,
}: Readonly<{ workflow: SelectedWorkflowBoard["selectedWorkflow"] }>) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);
  if (workflow.validForTaskCreation) {
    return null;
  }
  return (
    <FloatingNoticeIsland
      collapsed={collapsed}
      collapseLabel={t("app.collapse")}
      expandLabel={t("app.expand")}
      onCollapsedChange={setCollapsed}
      positionClassName="right-[var(--space-4)] bottom-[var(--space-4)]"
      title={t("board.workflowIssues")}
      tone="danger"
    >
      <WorkflowValidationIssues errors={workflow.validationErrors} />
    </FloatingNoticeIsland>
  );
}

import type { DragEvent, SyntheticEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { hasSelectedWorkflow, type BoardColumn, type SelectedWorkflowBoard } from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppNavigation } from "../../app/navigation";
import { useConnectionSnapshot } from "../../app/useConnectionSnapshot";
import { useSidebar } from "../../app/sidebarContext";
import { useAppServices } from "../../app/useAppServices";
import { useNativeDialogFallback } from "../../app/useNativeDialogFallback";
import { useStatusController } from "../../app/useStatusController";
import { useWindowChromeTitle } from "../../app/windowChromeTitle";
import { Button, EmptyState, ErrorState, FloatingNoticeIsland, LoadingState } from "../../ui";
import { WorkflowValidationIssues } from "../workflow/WorkflowValidationIssues";
import { ExecutionTargetContinuationDialog } from "../execution-target/ExecutionTargetContinuationDialog";
import { useExecutionTargetContinuation } from "../execution-target/useExecutionTargetContinuation";
import {
  executeExecutionTargetAction,
  moveExecutionTargetAction,
  startExecutionTargetAction,
  type ExecutionTargetContinuationAction,
} from "../execution-target/executionTargetContinuation";
import { BoardHoverMenu } from "./BoardHoverMenu";
import { BoardHorizontalScrollbar } from "./BoardHorizontalScrollbar";
import { useBoardDragAutoScroll } from "./BoardDragAutoScroll";
import { useBoardMoveRunFeedback } from "./BoardMoveRunFeedback";
import { BoardRailMotionController } from "./BoardRailMotionController";
import { TaskDeleteConfirmationFallbackDialog } from "./TaskDeleteConfirmation";
import { taskDeleteWindowOptions, type TaskDeleteTarget } from "./taskDeleteConfirmationModel";
import type { BoardColumnDropState } from "./BoardDragTypes";
import type { ActiveBoardCardDrag } from "./BoardDragState";
import {
  classifyDrop,
  isExecutableAutomationColumn,
  missingInputValues,
  type PendingDrop,
  type PendingMissingInputDrop,
} from "./BoardDropActions";
import type { PendingBoardCardMove } from "./BoardCardMotionModel";
import { MissingInputsDialog, RollbackStartDialog } from "./BoardDropDialogs";
import "./board.css";
import { useBoard, useBoardTaskActions, useProjectBoardSubscription } from "./useBoardData";

export type BoardRouteProps = Readonly<{
  projectId: string;
  workflowId: string | undefined;
  selectedTaskId: string;
}>;

const emptyExpandedEmptyColumnIDs: ReadonlySet<string> = new Set();

export function BoardRoute({ projectId, workflowId, selectedTaskId }: BoardRouteProps) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const navigation = useAppNavigation();
  const { activeDestination, closeSidebar } = useSidebar();
  const reportBoardLoadError = useCallback(
    (error: unknown) => {
      push({
        id: "board-load-error",
        tone: "danger",
        title: t("board.loadFailed"),
        body: errorMessage(error),
        durationMs: Infinity,
      });
    },
    [push, t],
  );
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

  if (boardQuery.isPending) {
    return <LoadingState chromePadding reveal={false} title={t("states.loading")} />;
  }
  if (boardQuery.isError) {
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

  return <BoardContent board={board} boardQueryWorkflowID={workflowId} selectedTaskId={selectedTaskId} />;
}

function BoardContent({
  board,
  boardQueryWorkflowID,
  selectedTaskId,
}: Readonly<{
  board: SelectedWorkflowBoard;
  boardQueryWorkflowID: string | undefined;
  selectedTaskId: string;
}>) {
  const { t } = useTranslation();
  const [workflowIssuesCollapsed, setWorkflowIssuesCollapsed] = useState(false);
  const [activeDrag, setActiveDrag] = useState<ActiveBoardCardDrag | null>(null);
  const [pendingCardMove, setPendingCardMove] = useState<PendingBoardCardMove | null>(null);
  const [expandedEmptyColumns, setExpandedEmptyColumns] = useState<
    Readonly<{ ids: ReadonlySet<string>; scope: string }>
  >(() => ({ ids: new Set(), scope: "" }));
  const [rollbackDrop, setRollbackDrop] = useState<PendingDrop | null>(null);
  const [missingInputDrop, setMissingInputDrop] = useState<PendingMissingInputDrop | null>(null);
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
  const { openSidebar } = useSidebar();
  const connection = useConnectionSnapshot();
  const actions = useBoardTaskActions(board.projectID, boardQueryWorkflowID, board.selectedWorkflow.id);
  const moveRunFeedback = useBoardMoveRunFeedback();
  const executionTargetContinuation = useExecutionTargetContinuation({
    execute: async (action, selection) => executeExecutionTargetAction(api, action, selection),
    onApplied: async (result) => {
      if (result.kind === "move" && result.response.outcome === "applied") {
        moveRunFeedback.trackMoveRunIDs(result.response.applied);
      }
      await actions.refresh();
    },
    onAppliedError: (error) => {
      push({
        body: errorMessage(error),
        durationMs: Infinity,
        id: "board-action-refresh-error",
        title: t("board.loadFailed"),
        tone: "danger",
      });
    },
  });
  const actionsDisabled =
    connection.phase !== "connected" ||
    executionTargetContinuation.running ||
    executionTargetContinuation.pending !== null;
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
  const reportActionError = useCallback(
    (id: string, title: string, error: unknown) => {
      const body = errorMessage(error);
      push({ id, tone: "danger", title, body, durationMs: Infinity });
    },
    [push],
  );
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
      if (active && result.status === "canceled" && result.reason === "closed") {
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
    window.addEventListener("blur", handleCancellation);
    return () => {
      document.removeEventListener("drop", handleDocumentDrop, true);
      document.removeEventListener("dragend", handleCancellation, true);
      window.removeEventListener("blur", handleCancellation);
    };
  }, [cancelActiveDrag, stopDragAutoScroll]);

  function dropTask(event: DragEvent<HTMLElement>, column: BoardColumn): void {
    event.preventDefault();
    const dragPayload = activeDrag === null ? null : activeDrag.payload;
    cancelActiveDrag();
    if (dragPayload === null || actionsDisabled || !board.selectedWorkflow.validForTaskCreation) {
      reportRejectedDrop();
      return;
    }
    const dropAction = classifyDrop(column, dragPayload, firstActive?.id);
    if (dropAction.kind === "start") {
      const pendingMove = { taskID: dragPayload.taskID, targetColumnID: column.id };
      runCardAction(startExecutionTargetAction(dragPayload.taskID), pendingMove);
      return;
    }
    if (dropAction.kind === "move") {
      const moveInput = {
        taskID: dragPayload.taskID,
        targetNodeID: column.id,
        ...(dropAction.allowMissingEdge === undefined
          ? {}
          : { allowMissingEdge: dropAction.allowMissingEdge }),
        ...(dropAction.autoApprove === undefined ? {} : { autoApprove: dropAction.autoApprove }),
      };
      const pendingMove = { taskID: dragPayload.taskID, targetColumnID: column.id };
      runCardAction(moveExecutionTargetAction(moveInput), pendingMove);
      return;
    }
    if (dropAction.kind === "confirmRollback") {
      setRollbackDrop({ taskID: dragPayload.taskID, targetColumn: column });
      return;
    }
    if (dropAction.kind === "reject") {
      reportRejectedDrop();
      return;
    }
    setMissingInputDrop({
      taskID: dragPayload.taskID,
      targetColumn: column,
      fields: column.transitionOutputFields,
      values: missingInputValues(column.transitionOutputFields),
    });
  }

  function interruptTask(taskID: string): void {
    void actions.interrupt.mutateAsync(taskID).catch(reportInterruptError);
  }

  function resumeTask(taskID: string): void {
    void actions.resume.mutateAsync(taskID).catch(reportResumeError);
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

  function reportStartError(error: unknown): void {
    reportActionError("board-start-error", t("board.startFailed"), error);
  }

  function reportMoveError(error: unknown): void {
    reportActionError("board-move-error", t("board.moveFailed"), error);
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
    if (actionsDisabled || !board.selectedWorkflow.validForTaskCreation) {
      return "blocked";
    }
    const manualTargets = new Set(activeDrag.payload.manualMoveTargetNodeIDs);
    const canStartHere = activeDrag.payload.canStart && column.id === firstActive?.id;
    return canStartHere || manualTargets.has(column.id) ? "allowed" : "blocked";
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

  function confirmRollbackDrop(): void {
    if (rollbackDrop === null) {
      return;
    }
    const drop = rollbackDrop;
    setRollbackDrop(null);
    const pendingMove = { taskID: drop.taskID, targetColumnID: drop.targetColumn.id };
    runCardAction(
      moveExecutionTargetAction({
        taskID: drop.taskID,
        targetNodeID: drop.targetColumn.id,
        autoApprove: true,
      }),
      pendingMove,
    );
  }

  function submitMissingInputDrop(event: SyntheticEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (missingInputDrop === null) {
      return;
    }
    const drop = missingInputDrop;
    setMissingInputDrop(null);
    const pendingMove = { taskID: drop.taskID, targetColumnID: drop.targetColumn.id };
    runCardAction(
      moveExecutionTargetAction({
        taskID: drop.taskID,
        targetNodeID: drop.targetColumn.id,
        outputValues: drop.values,
        allowMissingEdge: true,
        autoApprove: isExecutableAutomationColumn(drop.targetColumn),
      }),
      pendingMove,
    );
  }

  function runCardAction(action: ExecutionTargetContinuationAction, pendingMove: PendingBoardCardMove): void {
    setPendingCardMove(pendingMove);
    void executionTargetContinuation
      .run(action)
      .catch(action.kind === "start" ? reportStartError : reportMoveError)
      .finally(() => {
        clearPendingCardMove(pendingMove);
      });
  }

  function clearPendingCardMove(pendingMove: PendingBoardCardMove): void {
    setPendingCardMove((current) =>
      current?.taskID === pendingMove.taskID && current.targetColumnID === pendingMove.targetColumnID
        ? null
        : current,
    );
  }

  function openTask(taskID: string): void {
    void navigation
      .openProjectTask(board.projectID, board.selectedWorkflow.id, taskID)
      .catch(reportNavigationError);
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
    <div className="relative h-full min-h-0 min-w-0 w-full">
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
          onInterruptedRunObserved={moveRunFeedback.observeInterruptedRun}
          onInterruptTask={interruptTask}
          onRegisterColumnScrollport={dragAutoScroll.registerColumnScrollport}
          pendingCardMove={pendingCardMove}
          onResumeTask={resumeTask}
          scrollportRef={scrollportRef}
        />
      </div>
      <BoardHorizontalScrollbar scrollportRef={scrollportRef} />
      <RollbackStartDialog
        onClose={() => {
          setRollbackDrop(null);
        }}
        onConfirm={confirmRollbackDrop}
        open={rollbackDrop !== null}
      />
      <MissingInputsDialog
        drop={missingInputDrop}
        onClose={() => {
          setMissingInputDrop(null);
        }}
        onSubmit={submitMissingInputDrop}
        onValueChange={(fieldName, value) => {
          setMissingInputDrop((current) =>
            current === null ? null : { ...current, values: { ...current.values, [fieldName]: value } },
          );
        }}
      />
      <ExecutionTargetContinuationDialog continuation={executionTargetContinuation} />
      {taskDeleteDialog.fallback}
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

function BoardNoWorkflowState({ projectID }: Readonly<{ projectID: string }>) {
  const { t } = useTranslation();
  const { openSidebar } = useSidebar();
  const connection = useConnectionSnapshot();
  const actionsDisabled = connection.phase !== "connected";
  return (
    <EmptyState
      actions={
        <>
          <Button
            disabled={actionsDisabled}
            onClick={() => {
              void openSidebar({ kind: "linkWorkflow", mode: "overlay", projectID });
            }}
          >
            {t("workflowLibrary.linkWorkflow")}
          </Button>
          <Button
            disabled={actionsDisabled}
            onClick={() => {
              void openSidebar({ kind: "workflowCreate", mode: "overlay", projectID });
            }}
            variant="primary"
          >
            {t("workflowLibrary.createWorkflow")}
          </Button>
        </>
      }
      body={t("board.noWorkflowBody")}
      chromePadding
      title={t("board.noWorkflowTitle")}
    />
  );
}

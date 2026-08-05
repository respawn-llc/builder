import { useCallback, useLayoutEffect, useReducer } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";
import { taskDetailRouteShouldClose } from "./taskDetailRouteLifecycle";
import {
  boardTaskDeletionCauseMatches,
  boardTaskDeletionCauseShouldDefer,
  recordBoardTaskDeletionAttempt,
  settleBoardTaskDeletionAttempt,
  type BoardTaskDeletionAttempt,
  type BoardTaskDeletionCause,
} from "./boardTaskDeletionCause";

type CoordinatorState = Readonly<{
  committedTaskID: string | null;
  deletionCause: BoardTaskDeletionCause | null;
}>;

type CoordinatorAction =
  | Readonly<{ kind: "record"; attempt: BoardTaskDeletionAttempt }>
  | Readonly<{ kind: "settle"; attempt: BoardTaskDeletionAttempt; outcome: "failed" | "succeeded" }>
  | Readonly<{ kind: "commitSelector"; taskID: string | null }>;

const initialCoordinatorState: CoordinatorState = {
  committedTaskID: null,
  deletionCause: null,
};

function reduceCoordinatorState(state: CoordinatorState, action: CoordinatorAction): CoordinatorState {
  if (action.kind === "record") {
    return {
      ...state,
      deletionCause: recordBoardTaskDeletionAttempt(state.deletionCause, action.attempt),
    };
  }
  if (action.kind === "settle") {
    return {
      ...state,
      deletionCause: settleBoardTaskDeletionAttempt(
        state.deletionCause,
        action.attempt,
        action.outcome,
      ),
    };
  }
  return { committedTaskID: action.taskID, deletionCause: null };
}

export function useBoardSelectedTaskDeletion({
  enabled,
  onNavigationError,
  projectId,
  selectedTaskId,
  selectedWorkflowID,
}: Readonly<{
  enabled: boolean;
  onNavigationError(error: unknown): void;
  projectId: string;
  selectedTaskId: string | undefined;
  selectedWorkflowID: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const { closeSidebar, invalidateSidebar, openSidebar } = useSidebar();
  const [state, dispatch] = useReducer(reduceCoordinatorState, initialCoordinatorState);
  const request = useCallback(() => {
    if (!enabled || selectedTaskId === undefined || selectedWorkflowID === undefined) {
      return;
    }
    const attempt = { taskID: selectedTaskId };
    dispatch({ attempt, kind: "record" });
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, selectedWorkflowID).then((result) => {
      dispatch({
        attempt,
        kind: "settle",
        outcome: result.status === "completed" ? "succeeded" : "failed",
      });
      if (result.status === "failed") {
        onNavigationError(result.error);
      }
    }, (error: unknown) => {
      dispatch({ attempt, kind: "settle", outcome: "failed" });
      onNavigationError(error);
    });
  }, [
    dispatch,
    enabled,
    invalidateSidebar,
    navigation,
    onNavigationError,
    projectId,
    selectedTaskId,
    selectedWorkflowID,
  ]);

  useLayoutEffect(() => {
    if (!enabled || selectedWorkflowID === undefined) {
      return;
    }
    const next = selectedTaskId ?? null;
    const previous = state.committedTaskID;
    if (previous === next || boardTaskDeletionCauseShouldDefer(state.deletionCause, previous, next)) {
      return;
    }
    const preserveUnrelatedSidebar = boardTaskDeletionCauseMatches(state.deletionCause, previous, next);
    dispatch({ kind: "commitSelector", taskID: next });
    if (preserveUnrelatedSidebar) {
      return;
    }
    if (previous !== null || next === null) {
      closeSidebar("route_change");
    }
    if (next !== null) {
      void openSidebar({
        kind: "taskDetail",
        mode: "overlay",
        projectID: projectId,
        taskID: next,
      }).then((result) => {
        if (taskDetailRouteShouldClose(result)) {
          void navigation.closeProjectTask(projectId, selectedWorkflowID).then((result) => {
            if (result.status === "failed") {
              onNavigationError(result.error);
            }
          });
        }
      });
    }
  }, [
    closeSidebar,
    dispatch,
    enabled,
    navigation,
    onNavigationError,
    openSidebar,
    projectId,
    selectedTaskId,
    selectedWorkflowID,
    state,
  ]);

  return { request };
}

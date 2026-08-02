import { useCallback, type ReactElement } from "react";

import { ProjectEditRoute } from "@/features/project-edit";
import { TaskDetailSurface } from "@/features/task-detail";
import { NewTaskForm } from "@/features/tasks";
import { WorkflowEditorRoute, WorkflowInspectorSidebar } from "@/features/workflow-editor";
import { LinkWorkflowSidebar, WorkflowCreateForm } from "@/features/workflows";
import { useAppNavigation, useSidebar } from "@/app-facade";
import type { SidebarController, SidebarDestination } from "@/app-facade";

export function SidebarDestinationView({
  activeSnapshot,
  closeSidebar,
  destination,
  resolveSidebar,
}: Readonly<{
  activeSnapshot: SidebarController["activeSnapshot"];
  closeSidebar: SidebarController["closeSidebar"];
  destination: SidebarDestination;
  resolveSidebar: SidebarController["resolveSidebar"];
}>): ReactElement {
  const {
    activeToken,
    activeActivationID,
    removeSidebarEntry,
    replaceSidebarIfCurrent,
    resolveSidebarIfCurrent,
    setSidebarExitBlocked,
  } = useSidebar();
  const onSubmissionStateChange = useCallback(
    (pending: boolean) => {
      if (activeToken !== null) {
        setSidebarExitBlocked(activeToken, pending);
      }
    },
    [activeToken, setSidebarExitBlocked],
  );
  if (destination.kind === "newTask") {
    const formToken = activeToken;
    return (
      <NewTaskForm
        boardQueryWorkflowID={destination.boardQueryWorkflowID}
        className="w-full"
        initialSourceWorkspaceID={destination.initialSourceWorkspaceID}
        onSubmitted={(taskID) => {
          if (destination.pendingRelationship !== undefined && taskID !== undefined && formToken !== null) {
            replaceSidebarIfCurrent(formToken, {
              kind: "taskDetail",
              taskID,
              ...(destination.mode === undefined ? {} : { mode: destination.mode }),
            });
            return;
          }
          if (formToken !== null) {
            resolveSidebarIfCurrent(formToken, {
              destination: "newTask",
              status: "submitted",
            });
          }
        }}
        onSubmissionStateChange={onSubmissionStateChange}
        projectID={destination.projectID}
        pendingRelationship={destination.pendingRelationship}
        workflowID={destination.workflowID}
      />
    );
  }

  if (destination.kind === "taskDetail") {
    return (
      <TaskDetailSurface
        key={activeActivationID ?? undefined}
        enabled
        initialFocus={destination.initialFocus}
        sidebarSnapshot={activeSnapshot?.kind === "taskDetail" ? activeSnapshot : undefined}
        sidebarActivationID={activeActivationID}
        onMissingTask={() => {
          if (activeToken !== null) {
            removeSidebarEntry(activeToken);
          }
        }}
        onMutated={destination.onMutated}
        taskId={destination.taskID}
      />
    );
  }

  if (destination.kind === "workflowCreate") {
    return <WorkflowCreateDestinationView destination={destination} resolveSidebar={resolveSidebar} />;
  }

  if (destination.kind === "linkWorkflow") {
    return <LinkWorkflowDestinationView destination={destination} resolveSidebar={resolveSidebar} />;
  }

  if (destination.kind === "workflowInspect") {
    return (
      <WorkflowInspectorSidebar
        initialFocus={destination.initialFocus}
        onMissingSelectedNode={() => {
          closeSidebar("closed");
        }}
        selection={destination.selection}
        workflowID={destination.workflowID}
      />
    );
  }

  if (destination.kind === "workflowEditor") {
    return (
      <WorkflowEditorRoute
        projectID={destination.projectID ?? ""}
        surface="sidebar"
        workflowID={destination.workflowID}
      />
    );
  }

  if (destination.kind === "projectEdit") {
    return <ProjectEditRoute projectId={destination.projectID} />;
  }

  return <>{destination.content}</>;
}

function LinkWorkflowDestinationView({
  destination,
  resolveSidebar,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "linkWorkflow" }>;
  resolveSidebar: SidebarController["resolveSidebar"];
}>): ReactElement {
  const navigation = useAppNavigation();

  return (
    <LinkWorkflowSidebar
      onCreated={(workflowID) => {
        resolveSidebar({ destination: "workflow", status: "completed", workflowID });
        void navigation.openWorkflowEditor({
          projectID: destination.projectID,
          workflowID,
        });
      }}
      onLinked={(workflowID) => {
        resolveSidebar({ destination: "workflow", status: "completed", workflowID });
        void navigation.openProject(destination.projectID, workflowID);
      }}
      creating={destination.creating === true}
      projectID={destination.projectID}
      selectedWorkflowID={destination.selectedWorkflowID}
    />
  );
}

function WorkflowCreateDestinationView({
  destination,
  resolveSidebar,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "workflowCreate" }>;
  resolveSidebar: SidebarController["resolveSidebar"];
}>): ReactElement {
  const navigation = useAppNavigation();

  return (
    <WorkflowCreateForm
      onCreated={(result) => {
        resolveSidebar({ destination: "workflow", status: "completed", workflowID: result.workflow.id });
        void navigation.openWorkflowEditor({
          projectID: destination.projectID,
          workflowID: result.workflow.id,
        });
      }}
      projectID={destination.projectID}
    />
  );
}

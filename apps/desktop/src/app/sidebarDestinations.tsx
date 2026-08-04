import type { ReactElement } from "react";

import { ProjectEditRoute } from "@/features/project-edit";
import { TaskDetailSurface } from "@/features/task-detail";
import { NewTaskForm } from "@/features/tasks";
import { WorkflowEditorRoute, WorkflowInspectorSidebar } from "@/features/workflow-editor";
import { LinkWorkflowSidebar, WorkflowCreateForm } from "@/features/workflows";
import { useAppNavigation } from "@/app-facade";
import type { SidebarController, SidebarDestination } from "@/app-facade";
import { taskDetailSidebarDestination } from "./sidebarDestinationAdapter";
import { useSidebarHost } from "./sidebarHostContext";

export function SidebarDestinationView({
  closeSidebar,
  destination,
  resolveSidebar,
}: Readonly<{
  closeSidebar: SidebarController["closeSidebar"];
  destination: SidebarDestination;
  resolveSidebar: SidebarController["resolveSidebar"];
}>): ReactElement {
  const { actions, snapshot } = useSidebarHost();
  if (destination.kind === "newTask") {
    return (
      <NewTaskForm
        boardQueryWorkflowID={destination.boardQueryWorkflowID}
        className="w-full"
        initialSourceWorkspaceID={destination.initialSourceWorkspaceID}
        onSubmitAdmission={actions.admitMutation}
        onSubmitted={(taskID) => {
          if (destination.pendingRelationship !== undefined && taskID !== undefined) {
            actions.replace(
              taskDetailSidebarDestination(taskID, destination.projectID, { mode: destination.mode }),
            );
            return;
          }
          actions.resolve({ destination: "newTask", status: "submitted" });
        }}
        projectID={destination.projectID}
        pendingRelationship={destination.pendingRelationship}
        workflowID={destination.workflowID}
      />
    );
  }

  if (destination.kind === "taskDetail") {
    return (
      <TaskDetailSurface
        enabled
        initialFocus={destination.initialFocus}
        sidebarSnapshot={snapshot ?? undefined}
        onCaptureSidebarState={actions.capture}
        onMissingTask={() => {
          actions.invalidate();
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

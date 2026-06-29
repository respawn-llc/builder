import type { ReactElement } from "react";

import { ProjectEditRoute } from "../features/project-edit/ProjectEditRoute";
import { TaskDetailSurface } from "../features/task-detail/TaskDetailSurface";
import { NewTaskForm } from "../features/tasks/NewTaskDialog";
import { WorkflowInspectorSidebar } from "../features/workflow-editor/WorkflowInspectorSidebar";
import { WorkflowEditorRoute } from "../features/workflow-editor/WorkflowEditorRoute";
import { LinkWorkflowSidebar } from "../features/workflows/LinkWorkflowSidebar";
import { WorkflowCreateForm } from "../features/workflows/WorkflowCreateForm";
import { useAppNavigation } from "./navigation";
import type { SidebarController, SidebarDestination } from "./sidebarContext";

export function SidebarDestinationView({
  closeSidebar,
  destination,
  resolveSidebar,
}: Readonly<{
  closeSidebar: SidebarController["closeSidebar"];
  destination: SidebarDestination;
  resolveSidebar: SidebarController["resolveSidebar"];
}>): ReactElement {
  if (destination.kind === "newTask") {
    return (
      <NewTaskForm
        boardQueryWorkflowID={destination.boardQueryWorkflowID}
        className="w-full"
        onSubmitted={() => {
          resolveSidebar({ destination: "newTask", status: "submitted" });
        }}
        projectID={destination.projectID}
        workflowID={destination.workflowID}
      />
    );
  }

  if (destination.kind === "taskDetail") {
    return (
      <TaskDetailSurface
        enabled
        initialFocus={destination.initialFocus}
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
      selectedWorkflowID={destination.selectedWorkflowID ?? ""}
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

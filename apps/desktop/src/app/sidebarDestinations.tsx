import { PictureInPicture } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { ProjectDeleteButton, ProjectEditRoute } from "@/features/project-edit";
import { SidebarInboxNav } from "@/features/home";
import { TaskDetailSurface } from "@/features/task-detail";
import { NewTaskForm } from "@/features/tasks";
import { WorkflowEditorRoute, WorkflowInspectorSidebar } from "@/features/workflow-editor";
import { LinkWorkflowSidebar, WorkflowCreateForm } from "@/features/workflows";
import {
  useAppNavigation,
  useAppServices,
  usePublishSidebarHeaderAction,
  useStatusController,
  type SidebarDestination,
  type SidebarPageNavigator,
} from "@/app-facade";
import { IconTooltipButton } from "@/ui";
import { sidebarPopOutOptions } from "./sidebarPopOut";
import { sidebarTitle } from "@/app-facade";

export function SidebarDestinationView({
  destination,
  navigator,
  retainedState,
}: Readonly<{
  destination: SidebarDestination;
  navigator: SidebarPageNavigator;
  retainedState?: unknown;
}>): ReactElement {
  if (destination.kind === "newTask") return <NewTaskDestination destination={destination} navigator={navigator} />;
  if (destination.kind === "taskDetail") {
    return <TaskDetailDestination destination={destination} navigator={navigator} retainedState={retainedState} />;
  }
  if (destination.kind === "workflowCreate") return <WorkflowCreateDestinationView destination={destination} navigator={navigator} />;
  if (destination.kind === "linkWorkflow") return <LinkWorkflowDestinationView destination={destination} navigator={navigator} />;
  if (destination.kind === "workflowInspect") {
    return <WorkflowInspectorSidebar initialFocus={destination.initialFocus} onMissingSelectedNode={navigator.close} selection={destination.selection} workflowID={destination.workflowID} />;
  }
  if (destination.kind === "workflowEditor") return <WorkflowEditorRoute navigator={navigator} projectID={destination.projectID ?? ""} surface="sidebar" workflowID={destination.workflowID} />;
  if (destination.kind === "projectEdit") return <ProjectEditDestination destination={destination} navigator={navigator} />;
  return <>{destination.content}</>;
}

function TaskDetailDestination({ destination, navigator, retainedState }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "taskDetail" }>;
  navigator: SidebarPageNavigator;
  retainedState?: unknown;
}>): ReactElement {
  usePublishSidebarHeaderAction(
    <TaskDetailHeaderActions destination={destination} navigator={navigator} />,
  );
  return <TaskDetailSurface enabled initialFocus={destination.initialFocus} navigator={navigator} onMutated={destination.onMutated} retainedState={retainedState} sidebarMode={destination.mode} taskId={destination.taskID} />;
}

function TaskDetailHeaderActions({ destination, navigator }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "taskDetail" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const options = sidebarPopOutOptions(destination, sidebarTitle(destination, t));
  return (
    <>
      {destination.inboxNav === true ? <SidebarInboxNav destination={destination} navigator={navigator} /> : null}
      {options !== null && nativeBridge.capabilities.dialogWindows ? (
        <IconTooltipButton label={t("app.popOut")} onClick={() => {
          void nativeBridge.dialogs.openWindow(options).then(() => {
            navigator.close();
          }).catch((error: unknown) => {
            push({ id: "sidebar-popout-error", tone: "danger", title: t("app.popOutError"), body: errorMessage(error) });
          });
        }}>
          <PictureInPicture aria-hidden="true" size={18} strokeWidth={1.5} />
        </IconTooltipButton>
      ) : null}
    </>
  );
}

function NewTaskDestination({ destination, navigator }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "newTask" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const [pending, setPending] = useState(false);
  useEffect(
    () => navigator.registerAvailability({
      back: destination.pendingRelationship === undefined || !pending,
      close: destination.pendingRelationship === undefined || !pending,
    }),
    [destination.pendingRelationship, navigator, pending],
  );
  return (
    <NewTaskForm
      boardQueryWorkflowID={destination.boardQueryWorkflowID}
      className="w-full"
      initialSourceWorkspaceID={destination.initialSourceWorkspaceID}
      onPendingChange={setPending}
      onProjectMissing={navigator.back}
      onSubmitted={(taskID) => {
        if (destination.pendingRelationship === undefined) navigator.close();
        else navigator.replace({ kind: "taskDetail", taskID, ...(destination.mode === undefined ? {} : { mode: destination.mode }) });
      }}
      projectID={destination.projectID}
      pendingRelationship={destination.pendingRelationship}
      workflowID={destination.workflowID}
    />
  );
}

function ProjectEditDestination({ destination, navigator }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "projectEdit" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  usePublishSidebarHeaderAction(<ProjectDeleteButton navigator={navigator} projectID={destination.projectID} />);
  return <ProjectEditRoute navigator={navigator} projectId={destination.projectID} />;
}

function LinkWorkflowDestinationView({ destination, navigator }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "linkWorkflow" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const navigation = useAppNavigation();
  const follow = (action: () => Promise<void>) => {
    if (navigator.close() === "accepted") void action();
  };
  return (
    <LinkWorkflowSidebar
      onCreated={(workflowID) => {
        follow(async () => {
          await navigation.openWorkflowEditor({ projectID: destination.projectID, workflowID });
        });
      }}
      onLinked={(workflowID) => {
        follow(async () => {
          await navigation.openProject(destination.projectID, workflowID);
        });
      }}
      creating={destination.creating === true}
      navigator={navigator}
      projectID={destination.projectID}
      selectedWorkflowID={destination.selectedWorkflowID}
    />
  );
}

function WorkflowCreateDestinationView({ destination, navigator }: Readonly<{
  destination: Extract<SidebarDestination, { kind: "workflowCreate" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const navigation = useAppNavigation();
  return <WorkflowCreateForm onCreated={(result) => {
    if (navigator.close() === "accepted") void navigation.openWorkflowEditor({ projectID: destination.projectID, workflowID: result.workflow.id });
  }} onProjectMissing={navigator.back} projectID={destination.projectID} />;
}

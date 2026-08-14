import { PictureInPicture, Plus } from "lucide-react";
import { useEffect, useState, type ReactElement } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { ProjectDeleteButton, ProjectEditRoute } from "@/features/project-edit";
import { SidebarInboxNav } from "@/features/home";
import { TaskDetailSurface } from "@/features/task-detail";
import { NewTaskForm } from "@/features/tasks";
import {
  WorkflowDeleteButton,
  WorkflowEditorRoute,
  WorkflowInspectorSidebar,
} from "@/features/workflow-editor";
import { LinkWorkflowSidebar, WorkflowCreateForm } from "@/features/workflows";
import { writeClipboardText } from "@/shared/native-clipboard";
import {
  sidebarTitle,
  useAppNavigation,
  useAppServices,
  usePublishSidebarHeaderAction,
  useStatusController,
  type SidebarDestination,
  type SidebarPageNavigator,
} from "@/app-facade";
import { Button, CopyableValueButton, IconTooltipButton, showStatusToast } from "@/ui";
import { sidebarPopOutOptions } from "./sidebarPopOut";

export function SidebarDestinationView({
  destination,
  navigator,
  retainedState,
}: Readonly<{
  destination: SidebarDestination;
  navigator: SidebarPageNavigator;
  retainedState?: unknown;
}>): ReactElement {
  if (destination.kind === "newTask")
    return <NewTaskDestination destination={destination} navigator={navigator} />;
  if (destination.kind === "taskDetail") {
    return (
      <TaskDetailDestination destination={destination} navigator={navigator} retainedState={retainedState} />
    );
  }
  if (destination.kind === "workflowCreate")
    return <WorkflowCreateDestinationView destination={destination} navigator={navigator} />;
  if (destination.kind === "linkWorkflow")
    return <LinkWorkflowDestinationView destination={destination} navigator={navigator} />;
  if (destination.kind === "workflowInspect")
    return <WorkflowInspectorDestination destination={destination} navigator={navigator} />;
  if (destination.kind === "workflowSettings")
    return (
      <WorkflowEditorRoute
        navigator={navigator}
        projectID=""
        surface="settings"
        workflowID={destination.workflowID}
      />
    );
  if (destination.kind === "workflowEditor")
    return (
      <WorkflowEditorRoute
        navigator={navigator}
        projectID={destination.projectID ?? ""}
        surface="sidebar"
        workflowID={destination.workflowID}
      />
    );
  if (destination.kind === "projectEdit")
    return <ProjectEditDestination destination={destination} navigator={navigator} />;
  return <>{destination.content}</>;
}

function TaskDetailDestination({
  destination,
  navigator,
  retainedState,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "taskDetail" }>;
  navigator: SidebarPageNavigator;
  retainedState?: unknown;
}>): ReactElement {
  usePublishSidebarHeaderAction(<TaskDetailHeaderActions destination={destination} navigator={navigator} />);
  return (
    <TaskDetailSurface
      enabled
      initialFocus={destination.initialFocus}
      navigator={navigator}
      onMutated={destination.onMutated}
      retainedState={retainedState}
      sidebarDestination={destination}
      sidebarMode={destination.mode}
      taskId={destination.taskID}
    />
  );
}

function TaskDetailHeaderActions({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "taskDetail" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const options = sidebarPopOutOptions(destination, sidebarTitle(destination, t));
  return (
    <>
      {destination.inboxNav === true ? (
        <SidebarInboxNav destination={destination} navigator={navigator} />
      ) : null}
      {options !== null && nativeBridge.capabilities.dialogWindows ? (
        <IconTooltipButton
          label={t("app.popOut")}
          onClick={() => {
            void nativeBridge.dialogs
              .openWindow(options)
              .then(() => {
                navigator.close();
              })
              .catch((error: unknown) => {
                push({
                  id: "sidebar-popout-error",
                  tone: "danger",
                  title: t("app.popOutError"),
                  body: errorMessage(error),
                });
              });
          }}
        >
          <PictureInPicture aria-hidden="true" size={18} strokeWidth={1.5} />
        </IconTooltipButton>
      ) : null}
    </>
  );
}

function NewTaskDestination({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "newTask" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const [pending, setPending] = useState(false);
  useEffect(
    () =>
      navigator.registerAvailability({
        back: destination.pendingRelationship === undefined || !pending,
        close: destination.pendingRelationship === undefined || !pending,
      }),
    [destination.pendingRelationship, navigator, pending],
  );
  const formProps = {
    boardQueryWorkflowID: destination.boardQueryWorkflowID,
    className: "w-full",
    initialSourceWorkspaceID: destination.initialSourceWorkspaceID,
    onPendingChange: setPending,
    onProjectMissing: navigator.back,
    onSubmitted: (taskID: string) => {
      if (destination.pendingRelationship === undefined) {
        if (navigator.close() === "accepted") void destination.onCreated?.(taskID);
      }
      else
        navigator.replace({
          kind: "taskDetail",
          taskID,
          ...(destination.mode === undefined ? {} : { mode: destination.mode }),
        });
    },
    projectID: destination.projectID,
  };
  return (
    destination.workflowID === undefined ? (
      <NewTaskForm {...formProps} />
    ) : (
      <NewTaskForm
        {...formProps}
        pendingRelationship={destination.pendingRelationship}
        workflowID={destination.workflowID}
      />
    )
  );
}

function ProjectEditDestination({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "projectEdit" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  return (
    <ProjectEditRoute
      headerAccessory={<ProjectDeleteButton navigator={navigator} projectID={destination.projectID} />}
      navigator={navigator}
      projectId={destination.projectID}
    />
  );
}

function LinkWorkflowDestinationView({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "linkWorkflow" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const { t } = useTranslation();
  const { push } = useStatusController();
  usePublishSidebarHeaderAction(
    destination.creating === true ? null : (
      <LinkWorkflowCreateHeaderButton destination={destination} navigator={navigator} />
    ),
  );
  const complete = (completion: Parameters<typeof destination.onCompleted>[0]) => {
    if (navigator.close() !== "accepted") return;
    void (async () => destination.onCompleted(completion))().catch((error: unknown) => {
      push({ body: errorMessage(error), durationMs: Infinity, id: "workflow-link-completion-error", title: t("states.error"), tone: "danger" });
    });
  };
  return (
    <LinkWorkflowSidebar
      onCreated={(workflowID) => {
        complete({ kind: "created", workflowID });
      }}
      onLinked={(workflowID) => {
        complete({ kind: "linked", workflowID });
      }}
      creating={destination.creating === true}
      navigator={navigator}
      projectID={destination.projectID}
      selectedWorkflowID={destination.selectedWorkflowID}
    />
  );
}

function LinkWorkflowCreateHeaderButton({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "linkWorkflow" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const { t } = useTranslation();
  return (
    <Button
      aria-label={t("workflowLibrary.newWorkflow")}
      className="justify-self-end"
      onClick={() => {
        navigator.replace({ ...destination, creating: true });
      }}
      size="icon"
      title={t("workflowLibrary.newWorkflow")}
      variant="ghost"
    >
      <Plus aria-hidden="true" size={18} strokeWidth={1.6} />
    </Button>
  );
}

function WorkflowInspectorDestination({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "workflowInspect" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  usePublishSidebarHeaderAction(
    destination.selection.kind === "workflow" ? (
      <WorkflowDeleteButton onDeleted={navigator.close} workflowID={destination.workflowID} />
    ) : destination.selection.kind === "node" ? (
      <WorkflowEntityIDHeader entityID={destination.selection.nodeID} entityKind="node" />
    ) : destination.selection.kind === "edge" ? (
      <WorkflowEntityIDHeader entityID={destination.selection.edgeID} entityKind="edge" />
    ) : null,
  );
  return (
    <WorkflowInspectorSidebar
      initialFocus={destination.initialFocus}
      onMissingSelectedNode={navigator.close}
      selection={destination.selection}
      workflowID={destination.workflowID}
    />
  );
}

function WorkflowEntityIDHeader({
  entityID,
  entityKind,
}: Readonly<{
  entityID: string;
  entityKind: "edge" | "node";
}>): ReactElement {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const node = entityKind === "node";
  return (
    <CopyableValueButton
      accessibleLabel={
        node
          ? t("workflowEditor.copyNodeId", { id: entityID })
          : t("workflowEditor.copyEdgeId", { id: entityID })
      }
      className="max-w-full justify-self-end overflow-hidden text-ellipsis whitespace-nowrap font-mono text-xs"
      onActivate={() => {
        void writeClipboardText(entityID, nativeBridge)
          .then(() => {
            showStatusToast({
              id: `workflow-${entityKind}-id-copied-${entityID}`,
              title: node ? t("workflowEditor.nodeIdCopied") : t("workflowEditor.edgeIdCopied"),
              tone: "success",
            });
          })
          .catch(() => {
            showStatusToast({
              id: `workflow-${entityKind}-id-copy-failed-${entityID}`,
              title: node ? t("workflowEditor.nodeIdCopyFailed") : t("workflowEditor.edgeIdCopyFailed"),
              tone: "danger",
            });
          });
      }}
    >
      {entityID}
    </CopyableValueButton>
  );
}

function WorkflowCreateDestinationView({
  destination,
  navigator,
}: Readonly<{
  destination: Extract<SidebarDestination, { kind: "workflowCreate" }>;
  navigator: SidebarPageNavigator;
}>): ReactElement {
  const navigation = useAppNavigation();
  return (
    <WorkflowCreateForm
      onCreated={(result) => {
        if (navigator.close() === "accepted")
          void navigation.openWorkflowEditor({
            projectID: destination.projectID,
            workflowID: result.workflow.id,
          });
      }}
      onProjectMissing={navigator.back}
      projectID={destination.projectID}
    />
  );
}

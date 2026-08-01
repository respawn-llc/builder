import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useMatchRoute } from "@tanstack/react-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import type { WorkflowDeleteImpact } from "@/api";
import { errorMessage } from "@/api";
import {
  queryKeys,
  useAppNavigation,
  useAppServices,
  useConnectionSnapshot,
  useSidebar,
  useStatusController,
  type SidebarDestination,
} from "@/app-facade";
import { Dialog } from "@/ui";
import { WorkflowDeleteConfirmationContent } from "./WorkflowDeleteConfirmationContent";
import {
  workflowDeleteBlockersMessage,
  workflowDeleteDialogWidth,
  workflowDeleteInputFromImpact,
} from "./workflowDeleteShared";

type DeleteOperation = Readonly<{ impact: WorkflowDeleteImpact; ownerWorkflowID: string }>;
type PreviewAdmission = Readonly<{ ownerWorkflowID: string }>;

export function useWorkflowDeleteLauncher(workflowID: string): Readonly<{
  disabled: boolean;
  dialog: ReactNode;
  openWorkflowDelete: () => Promise<void>;
  opening: boolean;
}> {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const connection = useConnectionSnapshot();
  const navigation = useAppNavigation();
  const queryClient = useQueryClient();
  const matchRoute = useMatchRoute();
  const { activeDestination, closeSidebar } = useSidebar();
  const { push } = useStatusController();
  const [pending, setPending] = useState<DeleteOperation | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [committedOwner, setCommittedOwner] = useState<string | null>(null);
  const [openingOwner, setOpeningOwner] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const workflowIDRef = useRef(workflowID);
  const activeDestinationRef = useRef(activeDestination);
  const previewAdmissionRef = useRef<PreviewAdmission | null>(null);
  const submitAdmissionRef = useRef<DeleteOperation | null>(null);
  const committedOwnerRef = useRef<string | null>(null);
  useLayoutEffect(() => {
    workflowIDRef.current = workflowID;
    activeDestinationRef.current = activeDestination;
  }, [activeDestination, workflowID]);

  useEffect(() => {
    const stalePreview = previewAdmissionRef.current;
    const stalePending = pending;
    const previewStale = stalePreview !== null && stalePreview.ownerWorkflowID !== workflowID;
    const pendingStale =
      stalePending !== null &&
      stalePending.ownerWorkflowID !== workflowID &&
      submitAdmissionRef.current !== stalePending;
    if (!previewStale && !pendingStale) return;
    queueMicrotask(() => {
      if (previewStale && previewAdmissionRef.current === stalePreview) {
        previewAdmissionRef.current = null;
        setOpeningOwner((current) => (current === stalePreview.ownerWorkflowID ? null : current));
      }
      if (pendingStale) {
        setPending((current) => (current === stalePending ? null : current));
        setActionError(null);
      }
    });
  }, [pending, workflowID]);

  const confirmDelete = useCallback(
    async (operation: DeleteOperation): Promise<void> => {
      if (
        submitAdmissionRef.current !== null ||
        committedOwnerRef.current === operation.ownerWorkflowID ||
        workflowIDRef.current !== operation.ownerWorkflowID
      ) {
        return;
      }
      submitAdmissionRef.current = operation;
      setActionError(null);
      setSubmitting(true);
      const retry = (message: string): void => {
        if (submitAdmissionRef.current === operation) submitAdmissionRef.current = null;
        setSubmitting(false);
        if (workflowIDRef.current === operation.ownerWorkflowID) {
          setActionError(message);
        } else {
          setPending((current) => (current === operation ? null : current));
        }
      };
      try {
        const response = await api.deleteWorkflow(workflowDeleteInputFromImpact(operation.impact));
        if (!response.deleted) {
          retry(workflowDeleteBlockersMessage(response.blockers, t("workflowEditor.workflowDeleteBlocked")));
          return;
        }
      } catch (error) {
        retry(errorMessage(error));
        return;
      }

      committedOwnerRef.current = operation.ownerWorkflowID;
      setCommittedOwner(operation.ownerWorkflowID);
      submitAdmissionRef.current = null;
      setSubmitting(false);
      setPending((current) => (current === operation ? null : current));
      try {
        await invalidateWorkflowDeleteQueries(queryClient, operation.ownerWorkflowID);
        if (sidebarReferencesWorkflow(activeDestinationRef.current, operation.ownerWorkflowID)) {
          closeSidebar("closed");
        }
        const routeMatches =
          matchRoute({
            to: "/workflows/$workflowId/editor",
            params: { workflowId: operation.ownerWorkflowID },
            pending: false,
            fuzzy: false,
            includeSearch: false,
          }) !== false ||
          matchRoute({
            to: "/projects/$projectId",
            search: { workflowId: operation.ownerWorkflowID },
            pending: false,
            fuzzy: false,
            includeSearch: true,
          }) !== false;
        if (routeMatches) {
          if ((await navigation.openWorkflowLibrary()) === "failed") {
            throw new Error(t("workflowEditor.workflowDeleteNavigationError"));
          }
        }
        push({
          id: "workflow-delete-deleted",
          tone: "success",
          title: t("workflowEditor.workflowDeleted"),
        });
      } catch (error) {
        push({
          id: "workflow-delete-committed-notify-error",
          tone: "warning",
          title: t("workflowEditor.workflowDeleteTitle"),
          body: t("workflowEditor.workflowDeleteCommittedNotifyError", {
            message: errorMessage(error),
          }),
        });
      }
    },
    [api, closeSidebar, matchRoute, navigation, push, queryClient, t],
  );

  const openWorkflowDelete = useCallback(async (): Promise<void> => {
    if (
      previewAdmissionRef.current !== null ||
      submitAdmissionRef.current !== null ||
      pending?.ownerWorkflowID === workflowID ||
      committedOwnerRef.current === workflowID
    ) {
      return;
    }
    const admission = { ownerWorkflowID: workflowID };
    previewAdmissionRef.current = admission;
    setOpeningOwner(workflowID);
    try {
      const impact = await api.previewWorkflowDelete(workflowID);
      if (previewAdmissionRef.current === admission && workflowIDRef.current === workflowID) {
        setPending({ impact, ownerWorkflowID: workflowID });
      }
    } catch (error) {
      if (previewAdmissionRef.current === admission && workflowIDRef.current === workflowID) {
        push({
          id: "workflow-delete-preview-error",
          tone: "danger",
          title: t("workflowEditor.workflowDeleteTitle"),
          body: errorMessage(error),
        });
      }
    } finally {
      if (previewAdmissionRef.current === admission) previewAdmissionRef.current = null;
      setOpeningOwner((current) => (current === workflowID ? null : current));
    }
  }, [api, pending, push, t, workflowID]);

  const cancelPending = useCallback(() => {
    if (submitAdmissionRef.current !== null) return;
    setPending(null);
    setActionError(null);
  }, []);
  const currentPending = pending?.ownerWorkflowID === workflowID ? pending : null;
  const opening = openingOwner === workflowID;
  return {
    disabled:
      connection.phase !== "connected" ||
      opening ||
      submitting ||
      currentPending !== null ||
      committedOwner === workflowID,
    dialog:
      currentPending === null
        ? null
        : createPortal(
            <Dialog
              closeLabel={t("app.close")}
              closeDisabled={submitting}
              onClose={cancelPending}
              open
              title={t("workflowEditor.workflowDeleteTitle")}
              width={workflowDeleteDialogWidth}
            >
              <WorkflowDeleteConfirmationContent
                actionError={actionError ?? undefined}
                disabled={submitting}
                impact={currentPending.impact}
                onCancel={cancelPending}
                onConfirm={() => void confirmDelete(currentPending)}
              />
            </Dialog>,
            document.body,
          ),
    openWorkflowDelete,
    opening,
  };
}

function sidebarReferencesWorkflow(destination: SidebarDestination | null, workflowID: string): boolean {
  if (destination === null) return false;
  switch (destination.kind) {
    case "newTask":
    case "workflowInspect":
    case "workflowEditor":
      return destination.workflowID === workflowID;
    case "linkWorkflow":
      return destination.selectedWorkflowID === workflowID;
    case "taskDetail":
    case "workflowCreate":
    case "projectEdit":
    case "custom":
      return false;
  }
}

async function invalidateWorkflowDeleteQueries(queryClient: QueryClient, workflowID: string): Promise<void> {
  queryClient.removeQueries({ queryKey: queryKeys.workflowDefinition(workflowID) });
  queryClient.removeQueries({ queryKey: queryKeys.workflowValidation(workflowID, "execution") });
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflows }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowDefinitions }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowValidations }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allWorkflowGraphLayouts }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allProjectWorkflowLinks }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allBoards }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allAttention }),
    queryClient.invalidateQueries({ queryKey: queryKeys.allTasks }),
  ]);
}

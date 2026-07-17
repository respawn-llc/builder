import { useCallback, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowDeleteImpact } from "@/api";
import { errorMessage } from "@/api";
import {
  useAppServices,
  useConnectionSnapshot,
  useNativeDialogFallback,
  useStatusController,
} from "@/app-facade";
import { WorkflowDeleteFallbackDialog } from "./WorkflowDeleteFallbackDialog";
import {
  workflowDeleteBlockersMessage,
  workflowDeleteInputFromImpact,
  workflowDeleteWindowOptions,
  type WorkflowDeleteTarget,
} from "./workflowDeleteShared";

export function useWorkflowDeleteLauncher(workflowID: string): Readonly<{
  disabled: boolean;
  fallback: ReactNode;
  openWorkflowDelete: () => Promise<void>;
  opening: boolean;
}> {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const connection = useConnectionSnapshot();
  const { push } = useStatusController();
  const [opening, setOpening] = useState(false);
  const [fallbackSubmitting, setFallbackSubmitting] = useState(false);
  const fallbackSubmittingRef = useRef(false);
  const disabled = connection.phase !== "connected" || opening;

  const confirmDelete = useCallback(
    async (impact: WorkflowDeleteImpact, close: () => void): Promise<void> => {
      try {
        const response = await api.deleteWorkflow(workflowDeleteInputFromImpact(impact));
        if (!response.deleted) {
          push({
            id: "workflow-delete-blocked",
            tone: "danger",
            title: t("workflowEditor.workflowDeleteBlocked"),
            body: workflowDeleteBlockersMessage(response.blockers, t("workflowEditor.workflowDeleteBlocked")),
          });
          return;
        }
        close();
        await nativeBridge.workflowDeletion.notifyDeleted({ workflowID: impact.workflowID });
      } catch (error) {
        push({
          id: "workflow-delete-error",
          tone: "danger",
          title: t("workflowEditor.workflowDeleteTitle"),
          body: errorMessage(error),
        });
      }
    },
    [api, nativeBridge.workflowDeletion, push, t],
  );

  const confirmFallbackDelete = useCallback(
    async (impact: WorkflowDeleteImpact, close: () => void): Promise<void> => {
      if (fallbackSubmittingRef.current) {
        return;
      }
      fallbackSubmittingRef.current = true;
      setFallbackSubmitting(true);
      try {
        await confirmDelete(impact, close);
      } finally {
        fallbackSubmittingRef.current = false;
        setFallbackSubmitting(false);
      }
    },
    [confirmDelete],
  );

  const deleteDialog = useNativeDialogFallback<WorkflowDeleteTarget>({
    errorNoticeID: "workflow-delete-window-error",
    errorTitle: t("workflowEditor.workflowDeleteWindowError"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (target) => {
      await nativeBridge.dialogs.openWindow(
        workflowDeleteWindowOptions(target.impact, t("workflowEditor.workflowDeleteTitle")),
      );
    },
    renderFallback: (target, close) => (
      <WorkflowDeleteFallbackDialog
        disabled={fallbackSubmitting}
        impact={target.impact}
        onCancel={close}
        onConfirm={() => void confirmFallbackDelete(target.impact, close)}
      />
    ),
  });

  const openWorkflowDelete = useCallback(
    async () =>
      openWorkflowDeleteConfirmation({
        open: deleteDialog.open,
        preview: async (id) => api.previewWorkflowDelete(id),
        setOpening,
        pushError: (message) => {
          push({
            id: "workflow-delete-preview-error",
            tone: "danger",
            title: t("workflowEditor.workflowDeleteTitle"),
            body: message,
          });
        },
        workflowID,
      }),
    [api, deleteDialog.open, push, t, workflowID],
  );

  return {
    disabled,
    fallback: deleteDialog.fallback,
    openWorkflowDelete,
    opening,
  };
}

async function openWorkflowDeleteConfirmation({
  open,
  preview,
  setOpening,
  pushError,
  workflowID,
}: Readonly<{
  open: (target: WorkflowDeleteTarget) => Promise<void>;
  preview: (workflowID: string) => Promise<WorkflowDeleteImpact>;
  setOpening: (opening: boolean) => void;
  pushError: (message: string) => void;
  workflowID: string;
}>): Promise<void> {
  setOpening(true);
  try {
    await open({ impact: await preview(workflowID) });
  } catch (error) {
    pushError(errorMessage(error));
  } finally {
    setOpening(false);
  }
}

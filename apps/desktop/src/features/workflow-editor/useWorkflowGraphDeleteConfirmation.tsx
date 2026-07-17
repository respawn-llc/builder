import { useCallback, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { WorkflowDeleteConfirmationFallbackDialog } from "./WorkflowDeleteConfirmationWindow";
import {
  workflowDeleteConfirmationTextKeys,
  workflowDeleteConfirmationWindowOptions,
} from "./workflowDeleteConfirmationModel";
import { workflowDeletionConfirmationCounts } from "./workflowDeleteConfirmationPolicy";
import { useWorkflowGraphDeleteConfirmationListener } from "./useWorkflowGraphDeleteConfirmationListener";
import {
  cascadeSummaryEquals,
  confirmationOperation,
  dispatchPendingGraphMutation,
  planPendingGraphMutation,
  type PendingGraphMutation,
} from "./workflowEditorGraphMutationPlanning";
import type { WorkflowEditorDraftAction, WorkflowEditorDraftState } from "./workflowEditorDraft";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";

export type WorkflowGraphDeleteConfirmation = Readonly<{
  fallback: ReactNode;
  open: (mutation: PendingGraphMutation) => Promise<void>;
}>;

export function useWorkflowGraphDeleteConfirmation(
  params: Readonly<{
    closeDeletedNodeInspector: (selection: WorkflowGraphSelection) => void;
    dispatch: (action: WorkflowEditorDraftAction) => void;
    draftState: WorkflowEditorDraftState | null;
    onPendingGraphMutationChange: (mutation: PendingGraphMutation | null) => void;
    pendingRef: { current: PendingGraphMutation | null };
  }>,
): WorkflowGraphDeleteConfirmation {
  const { closeDeletedNodeInspector, dispatch, draftState, onPendingGraphMutationChange, pendingRef } =
    params;
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push: pushStatus } = useStatusController();

  const confirmPendingGraphMutation = useCallback(
    (mutationRequest: PendingGraphMutation) => {
      onPendingGraphMutationChange(null);
      if (draftState === null) {
        return;
      }
      const currentPlan = planPendingGraphMutation(draftState, mutationRequest);
      const rejectStaleConfirmation = () => {
        pushStatus({
          body: t("workflowEditor.deleteConfirmationStale"),
          id: "workflow-delete-confirmation-stale",
          title: t("workflowEditor.deleteBlockedTitle"),
          tone: "warning",
        });
      };
      if (currentPlan.kind !== "ready") {
        rejectStaleConfirmation();
        return;
      }
      const currentCounts = workflowDeletionConfirmationCounts(draftState.draft, currentPlan.summary);
      if (
        !cascadeSummaryEquals(currentPlan.summary, mutationRequest.summary) ||
        currentCounts.promptCount !== mutationRequest.counts.promptCount
      ) {
        rejectStaleConfirmation();
        return;
      }
      dispatchPendingGraphMutation(mutationRequest, dispatch);
      if (mutationRequest.action.kind === "delete") {
        closeDeletedNodeInspector(mutationRequest.action.selection);
      }
    },
    [closeDeletedNodeInspector, dispatch, draftState, onPendingGraphMutationChange, pushStatus, t],
  );

  const handleListenerError = useCallback(
    (error: unknown) => {
      pushStatus({
        body: t("workflowEditor.deleteConfirmationListenerFailed", { message: errorMessage(error) }),
        id: "workflow-delete-confirmation-listener-failed",
        title: t("workflowEditor.deleteBlockedTitle"),
        tone: "danger",
      });
    },
    [pushStatus, t],
  );

  useWorkflowGraphDeleteConfirmationListener({
    nativeBridge,
    onConfirmed: confirmPendingGraphMutation,
    onListenerError: handleListenerError,
    pendingDeleteRef: pendingRef,
  });

  const deleteConfirmation = useNativeDialogFallback<PendingGraphMutation>({
    errorNoticeID: "workflow-delete-confirmation-window-error",
    errorTitle: t("workflowEditor.deleteCascadeTitle"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (deleteRequest) => {
      const operation = confirmationOperation(deleteRequest);
      const textKeys = workflowDeleteConfirmationTextKeys(deleteRequest.counts, operation);
      await nativeBridge.dialogs.openWindow(
        workflowDeleteConfirmationWindowOptions({
          counts: deleteRequest.counts,
          operation,
          requestID: deleteRequest.requestID,
          title: t(textKeys.titleKey),
        }),
      );
    },
    renderFallback: (mutationRequest, close) => (
      <WorkflowDeleteConfirmationFallbackDialog
        counts={mutationRequest.counts}
        onCancel={() => {
          onPendingGraphMutationChange(null);
          close();
        }}
        onConfirm={() => {
          confirmPendingGraphMutation(mutationRequest);
          close();
        }}
        operation={confirmationOperation(mutationRequest)}
      />
    ),
  });

  return {
    fallback: deleteConfirmation.fallback,
    open: deleteConfirmation.open,
  };
}

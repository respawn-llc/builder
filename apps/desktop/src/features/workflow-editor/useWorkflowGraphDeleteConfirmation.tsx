import { useCallback, useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { useStatusController } from "@/app-facade";
import { WorkflowGraphDeleteConfirmationDialog } from "./WorkflowGraphDeleteConfirmationDialog";
import { workflowDeletionConfirmationCounts } from "./workflowDeleteConfirmationPolicy";
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
  dialog: ReactNode;
  open: (mutation: PendingGraphMutation) => void;
}>;

type PendingConfirmation = Readonly<{
  mutation: PendingGraphMutation;
  ownerWorkflowID: string;
}>;

export function useWorkflowGraphDeleteConfirmation(
  params: Readonly<{
    closeDeletedNodeInspector: (selection: WorkflowGraphSelection) => void;
    dispatch: (action: WorkflowEditorDraftAction) => void;
    draftState: WorkflowEditorDraftState | null;
    workflowID: string;
  }>,
): WorkflowGraphDeleteConfirmation {
  const { closeDeletedNodeInspector, dispatch, draftState, workflowID } = params;
  const { t } = useTranslation();
  const { push: pushStatus } = useStatusController();
  const [pending, setPending] = useState<PendingConfirmation | null>(null);

  useEffect(() => {
    if (pending === null || pending.ownerWorkflowID === workflowID) {
      return;
    }
    const stalePending = pending;
    queueMicrotask(() => {
      setPending((current) => (current === stalePending ? null : current));
    });
  }, [pending, workflowID]);

  const confirmPendingGraphMutation = useCallback(
    (mutationRequest: PendingGraphMutation) => {
      setPending(null);
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
      if (mutationRequest.action.kind === "delete" && mutationRequest.action.selection.kind === "node") {
        closeDeletedNodeInspector(mutationRequest.action.selection);
      }
    },
    [closeDeletedNodeInspector, dispatch, draftState, pushStatus, t],
  );

  const currentPending = pending?.ownerWorkflowID === workflowID ? pending : null;
  return {
    dialog:
      currentPending === null
        ? null
        : createPortal(
            <WorkflowGraphDeleteConfirmationDialog
              counts={currentPending.mutation.counts}
              onCancel={() => {
                setPending(null);
              }}
              onConfirm={() => {
                confirmPendingGraphMutation(currentPending.mutation);
              }}
              operation={confirmationOperation(currentPending.mutation)}
            />,
            document.body,
          ),
    open: useCallback(
      (mutation) => {
        setPending({ mutation, ownerWorkflowID: workflowID });
      },
      [workflowID],
    ),
  };
}

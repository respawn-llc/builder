import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { Button, Dialog } from "@/ui";
import {
  workflowDeleteConfirmationTextKeys,
  type WorkflowDeleteConfirmationCounts,
  type WorkflowGraphCascadeConfirmationOperation,
  type WorkflowDeleteConfirmationWindowTarget,
} from "./workflowDeleteConfirmationModel";

export function WorkflowDeleteConfirmationFallbackDialog({
  counts,
  onCancel,
  onConfirm,
  operation = "delete",
}: Readonly<{
  counts: WorkflowDeleteConfirmationCounts;
  onCancel: () => void;
  onConfirm: () => void;
  operation?: WorkflowGraphCascadeConfirmationOperation | undefined;
}>) {
  const { t } = useTranslation();
  const textKeys = workflowDeleteConfirmationTextKeys(counts, operation);
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onCancel}
      open
      style={{ width: "min(420px, calc(100vw - 32px))" }}
      title={t(textKeys.titleKey)}
    >
      <WorkflowDeleteConfirmationContent
        counts={counts}
        onCancel={onCancel}
        onConfirm={onConfirm}
        operation={operation}
      />
    </Dialog>
  );
}

export function WorkflowDeleteConfirmationWindowRoute({
  counts,
  operation,
  requestID,
}: WorkflowDeleteConfirmationWindowTarget) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const [actionError, setActionError] = useState("");
  const textKeys = workflowDeleteConfirmationTextKeys(counts, operation);
  return (
    <NativeDialogWindow contentMaxWidth="420px" title={t(textKeys.titleKey)}>
      <WorkflowDeleteConfirmationContent
        actionError={actionError}
        counts={counts}
        onCancel={() => {
          setActionError("");
          void nativeBridge.window.closeCurrent().catch((error: unknown) => {
            setActionError(errorMessage(error));
          });
        }}
        onConfirm={() => {
          setActionError("");
          void confirmWorkflowGraphDelete(nativeBridge, requestID).catch((error: unknown) => {
            setActionError(errorMessage(error));
          });
        }}
        operation={operation}
      />
    </NativeDialogWindow>
  );
}

function WorkflowDeleteConfirmationContent({
  actionError,
  counts,
  onCancel,
  onConfirm,
  operation,
}: Readonly<{
  actionError?: string | undefined;
  counts: WorkflowDeleteConfirmationCounts;
  onCancel: () => void;
  onConfirm: () => void;
  operation: WorkflowGraphCascadeConfirmationOperation;
}>) {
  const { t } = useTranslation();
  const textKeys = workflowDeleteConfirmationTextKeys(counts, operation);
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t(textKeys.bodyKey)}</p>
      {counts.promptCount > 0 ? (
        <p className="m-0 text-sm text-[var(--color-error)]">{t("workflowEditor.deletePromptLossWarning")}</p>
      ) : null}
      {actionError === undefined || actionError.length === 0 ? null : (
        <p className="m-0 text-sm text-[var(--color-error)]">{actionError}</p>
      )}
      <ul className="m-0 grid gap-[var(--space-1)] p-0 text-sm text-[var(--color-muted)]">
        <li className="list-none">{t("workflowEditor.deleteCascadeNodes", { count: counts.nodeCount })}</li>
        <li className="list-none">{t("workflowEditor.deleteCascadeEdges", { count: counts.edgeCount })}</li>
        {counts.promptCount > 0 ? (
          <li className="list-none">
            {t("workflowEditor.deleteCascadePrompts", { count: counts.promptCount })}
          </li>
        ) : null}
        <li className="list-none">
          {t("workflowEditor.deleteCascadeTransitionGroups", { count: counts.transitionGroupCount })}
        </li>
      </ul>
      <div className="grid grid-cols-2 gap-[var(--space-2)]">
        <Button className="w-full" onClick={onCancel} variant="secondary">
          {t("app.cancel")}
        </Button>
        <Button className="w-full" onClick={onConfirm} variant="danger">
          {t(textKeys.confirmKey)}
        </Button>
      </div>
    </div>
  );
}

async function confirmWorkflowGraphDelete(
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"],
  requestID: string,
): Promise<void> {
  await nativeBridge.workflowEditor.confirmGraphDelete({ requestID });
  await nativeBridge.window.closeCurrent();
}

import { useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import {
  useWorkflowDeleteLauncher,
  WorkflowDeleteConfirmationContent,
  workflowDeleteBlockersMessage,
  workflowDeleteDialogWidth,
  workflowDeleteInputFromImpact,
  type WorkflowDeleteTarget,
} from "@/shared/workflow-deletion";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { Button } from "@/ui";

export function WorkflowDeleteButton({ workflowID }: Readonly<{ workflowID: string }>) {
  const { t } = useTranslation();
  const deleteLauncher = useWorkflowDeleteLauncher(workflowID);

  return (
    <>
      {deleteLauncher.fallback}
      <Button
        aria-label={t("workflowEditor.workflowDelete")}
        className="justify-self-end"
        disabled={deleteLauncher.disabled}
        onClick={() => {
          void deleteLauncher.openWorkflowDelete();
        }}
        size="icon"
        title={t("workflowEditor.workflowDelete")}
        variant="danger"
      >
        <Trash2 aria-hidden="true" className="block" size={18} strokeWidth={1.5} />
      </Button>
    </>
  );
}

export function WorkflowDeleteWindowRoute({ impact }: WorkflowDeleteTarget) {
  const { t } = useTranslation();
  const { api, nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const [actionError, setActionError] = useState("");
  const [committed, setCommitted] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
  const confirmDelete = useCallback(async (): Promise<void> => {
    if (submittingRef.current || committed) {
      return;
    }
    submittingRef.current = true;
    setActionError("");
    setSubmitting(true);
    try {
      const response = await api.deleteWorkflow(workflowDeleteInputFromImpact(impact));
      if (!response.deleted) {
        setActionError(
          workflowDeleteBlockersMessage(response.blockers, t("workflowEditor.workflowDeleteBlocked")),
        );
        submittingRef.current = false;
        setSubmitting(false);
        return;
      }
      setCommitted(true);
      try {
        await nativeBridge.workflowDeletion.notifyDeleted({ workflowID: impact.workflowID });
      } catch (error) {
        const message = t("workflowEditor.workflowDeleteCommittedNotifyError", {
          message: errorMessage(error),
        });
        setActionError(message);
        push({
          id: "workflow-delete-committed-notify-error",
          tone: "warning",
          title: t("workflowEditor.workflowDeleteWindowError"),
          body: message,
        });
        submittingRef.current = false;
        setSubmitting(false);
        return;
      }
      try {
        await nativeBridge.window.closeCurrent();
      } catch (error) {
        const message = t("workflowEditor.workflowDeleteCommittedCloseError", {
          message: errorMessage(error),
        });
        setActionError(message);
        push({
          id: "workflow-delete-committed-close-error",
          tone: "warning",
          title: t("workflowEditor.workflowDeleteWindowError"),
          body: message,
        });
        submittingRef.current = false;
        setSubmitting(false);
      }
    } catch (error) {
      setActionError(errorMessage(error));
      push({
        id: "workflow-delete-window-error",
        tone: "danger",
        title: t("workflowEditor.workflowDeleteWindowError"),
        body: errorMessage(error),
      });
      submittingRef.current = false;
      setSubmitting(false);
    }
  }, [api, committed, impact, nativeBridge.workflowDeletion, nativeBridge.window, push, t]);

  return (
    <NativeDialogWindow
      contentMaxWidth={`${workflowDeleteDialogWidth.toString()}px`}
      title={t("workflowEditor.workflowDeleteTitle")}
    >
      <WorkflowDeleteConfirmationContent
        actionError={actionError}
        committed={committed}
        disabled={submitting}
        impact={impact}
        onCancel={() => {
          void nativeBridge.window.closeCurrent();
        }}
        onConfirm={() => void confirmDelete()}
      />
    </NativeDialogWindow>
  );
}

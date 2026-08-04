import { useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";

import { errorMessage } from "@/api";
import { useAppNavigation } from "@/app-facade";
import { completeProjectDeletion } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { useNativeDialogFallback } from "@/app-facade";
import { useSidebar } from "@/app-facade";
import { useStatusController } from "@/app-facade";
import { NativeDialogWindow } from "@/shared/native-dialog";
import { Button, compactDialogWidth, Dialog } from "@/ui";
import { useProjectDelete } from "./useProjectEditData";

const projectDeleteNativeDialogPath = "/native-dialog/project-delete";
const projectDeleteDialogWidth = compactDialogWidth;

type ProjectDeleteTarget = Readonly<{
  projectID: string;
}>;

export function ProjectDeleteButton({ projectID }: Readonly<{ projectID: string }>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const connection = useConnectionSnapshot();
  const { closeSidebar, invalidateSidebar } = useSidebar();
  const navigation = useAppNavigation();
  const { push } = useStatusController();
  const queryClient = useQueryClient();
  const mutation = useProjectDelete(projectID, { invalidateOnDeleted: false });
  const disabled = connection.phase !== "connected" || mutation.isPending;

  const confirmDelete = useCallback(
    async (close: () => void) => {
      try {
        const response = await mutation.mutateAsync();
        if (!response.deleted) {
          push({
            id: "project-delete-blocked",
            tone: "danger",
            title: t("projectEdit.deleteBlocked"),
            body: response.blockers.map((blocker) => blocker.message).join("\n"),
          });
          return;
        }
        close();
        await completeProjectDeletion({
          closeSidebar,
          invalidateSidebar,
          navigateHome: navigation.openHome,
          projectID,
          pushDeletedToast: () => {
            push({
              id: "project-delete-deleted",
              tone: "success",
              title: t("projectEdit.deleteDeleted"),
            });
          },
          queryClient,
        });
      } catch (error) {
        push({
          id: "project-delete-error",
          tone: "danger",
          title: t("projectEdit.deleteTitle"),
          body: errorMessage(error),
        });
      }
    },
    [closeSidebar, invalidateSidebar, mutation, navigation.openHome, projectID, push, queryClient, t],
  );

  const deleteDialog = useNativeDialogFallback<ProjectDeleteTarget>({
    errorNoticeID: "project-delete-window-error",
    errorTitle: t("projectEdit.deleteWindowError"),
    nativeAvailable: nativeBridge.capabilities.dialogWindows,
    openNative: async (target) => {
      await nativeBridge.dialogs.openWindow(projectDeleteWindowOptions(target, t("projectEdit.deleteTitle")));
    },
    renderFallback: (_target, close) => (
      <ProjectDeleteConfirmationFallbackDialog
        disabled={mutation.isPending}
        onClose={close}
        onConfirm={() => void confirmDelete(close)}
      />
    ),
  });

  return (
    <>
      {deleteDialog.fallback}
      <Button
        aria-label={t("projectEdit.deleteProject")}
        className="justify-self-end"
        disabled={disabled}
        onClick={() => {
          void deleteDialog.open({ projectID });
        }}
        size="icon"
        title={t("projectEdit.deleteProject")}
        variant="danger"
      >
        <Trash2 aria-hidden="true" className="block" size={18} strokeWidth={1.5} />
      </Button>
    </>
  );
}

export function ProjectDeleteWindowRoute({ projectID }: ProjectDeleteTarget) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const mutation = useProjectDelete(projectID, { invalidateOnDeleted: false });
  const [actionError, setActionError] = useState("");
  const [committed, setCommitted] = useState(false);
  const submittedRef = useRef(false);
  const confirmDelete = useCallback(async (): Promise<void> => {
    if (committed || submittedRef.current) {
      return;
    }
    submittedRef.current = true;
    setActionError("");
    try {
      const response = await mutation.mutateAsync();
      if (!response.deleted) {
        submittedRef.current = false;
        setActionError(
          response.blockers.map((blocker) => blocker.message).join("\n") || t("projectEdit.deleteBlocked"),
        );
        return;
      }
      setCommitted(true);
      try {
        await nativeBridge.projectDeletion.notifyDeleted({ projectID });
      } catch (error) {
        const message = t("projectEdit.deleteCommittedNotifyError", { message: errorMessage(error) });
        setActionError(message);
        push({
          id: "project-delete-committed-notify-error",
          tone: "warning",
          title: t("projectEdit.deleteWindowError"),
          body: message,
        });
        return;
      }
      try {
        await nativeBridge.window.closeCurrent();
      } catch (error) {
        const message = t("projectEdit.deleteCommittedCloseError", { message: errorMessage(error) });
        setActionError(message);
        push({
          id: "project-delete-committed-close-error",
          tone: "warning",
          title: t("projectEdit.deleteWindowError"),
          body: message,
        });
      }
    } catch (error) {
      submittedRef.current = false;
      setActionError(errorMessage(error));
      push({
        id: "project-delete-window-error",
        tone: "danger",
        title: t("projectEdit.deleteWindowError"),
        body: errorMessage(error),
      });
    }
  }, [committed, mutation, nativeBridge.projectDeletion, nativeBridge.window, projectID, push, t]);

  return (
    <NativeDialogWindow
      contentMaxWidth={`${projectDeleteDialogWidth.toString()}px`}
      title={t("projectEdit.deleteTitle")}
    >
      <ProjectDeleteConfirmationContent
        actionError={actionError}
        committed={committed}
        disabled={mutation.isPending}
        onCancel={() => {
          void nativeBridge.window.closeCurrent();
        }}
        onConfirm={() => void confirmDelete()}
      />
    </NativeDialogWindow>
  );
}

function ProjectDeleteConfirmationFallbackDialog({
  disabled,
  onClose,
  onConfirm,
}: Readonly<{
  disabled: boolean;
  onClose: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onClose}
      open
      title={t("projectEdit.deleteTitle")}
      width={projectDeleteDialogWidth}
    >
      <ProjectDeleteConfirmationContent disabled={disabled} onCancel={onClose} onConfirm={onConfirm} />
    </Dialog>
  );
}

function ProjectDeleteConfirmationContent({
  actionError,
  committed = false,
  disabled,
  onCancel,
  onConfirm,
}: Readonly<{
  actionError?: string | undefined;
  committed?: boolean | undefined;
  disabled: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t("projectEdit.deleteBody")}</p>
      {actionError === undefined || actionError.length === 0 ? null : (
        <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-error)]">{actionError}</p>
      )}
      {committed ? (
        <Button className="justify-self-end" onClick={onCancel}>
          {t("app.close")}
        </Button>
      ) : (
        <div className="grid grid-cols-2 gap-[var(--space-2)]">
          <Button className="w-full" disabled={disabled} onClick={onCancel}>
            {t("app.cancel")}
          </Button>
          <Button className="w-full" disabled={disabled} onClick={onConfirm} variant="danger">
            {t("projectEdit.deleteConfirm")}
          </Button>
        </div>
      )}
    </div>
  );
}

function projectDeleteWindowOptions(target: ProjectDeleteTarget, title: string) {
  return {
    initialHeight: 260,
    initialWidth: projectDeleteDialogWidth,
    label: `project-delete-${target.projectID}`,
    params: {
      projectID: target.projectID,
    },
    route: projectDeleteNativeDialogPath,
    title,
  };
}

import { useTranslation } from "react-i18next";

import { Button, Dialog } from "@/ui";
import { taskDeleteDialogWidth } from "./taskDeleteDialogLayout";

export function TaskDeleteConfirmationDialog({
  actionError = null,
  disabled,
  onClose,
  onConfirm,
}: Readonly<{
  actionError?: string | null;
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
      title={t("board.deleteTaskTitle")}
      width={taskDeleteDialogWidth}
    >
      <TaskDeleteConfirmationContent
        actionError={actionError}
        disabled={disabled}
        onCancel={onClose}
        onConfirm={onConfirm}
      />
    </Dialog>
  );
}

export function TaskDeleteConfirmationContent({
  actionError = null,
  disabled,
  onCancel,
  onConfirm,
}: Readonly<{
  actionError?: string | null;
  disabled: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t("board.deleteTaskBody")}</p>
      {actionError !== null ? (
        <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-error)]">{actionError}</p>
      ) : null}
      <div className="grid grid-cols-2 gap-[var(--space-2)]">
        <Button className="w-full" disabled={disabled} onClick={onCancel}>
          {t("app.cancel")}
        </Button>
        <Button className="w-full" disabled={disabled} onClick={onConfirm} variant="danger">
          {t("board.deleteTaskConfirm")}
        </Button>
      </div>
    </div>
  );
}

import { useTranslation } from "react-i18next";

import type { WorkflowDeleteImpact } from "@/api";
import { Dialog } from "@/ui";
import { WorkflowDeleteConfirmationContent } from "./WorkflowDeleteConfirmationContent";
import { workflowDeleteDialogWidth } from "./workflowDeleteShared";

export function WorkflowDeleteFallbackDialog({
  impact,
  disabled,
  onCancel,
  onConfirm,
}: Readonly<{
  impact: WorkflowDeleteImpact;
  disabled: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onCancel}
      open
      style={{ width: `min(${workflowDeleteDialogWidth.toString()}px, calc(100vw - 32px))` }}
      title={t("workflowEditor.workflowDeleteTitle")}
    >
      <WorkflowDeleteConfirmationContent
        disabled={disabled}
        impact={impact}
        onCancel={onCancel}
        onConfirm={onConfirm}
      />
    </Dialog>
  );
}

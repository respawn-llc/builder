import { useTranslation } from "react-i18next";

import type { WorkflowDeleteImpact } from "@/api";
import { Button } from "@/ui";

export function WorkflowDeleteConfirmationContent({
  actionError,
  disabled = false,
  impact,
  onCancel,
  onConfirm,
}: Readonly<{
  actionError?: string | undefined;
  disabled?: boolean | undefined;
  impact: WorkflowDeleteImpact;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t("workflowEditor.workflowDeleteBody")}</p>
      {actionError === undefined || actionError.length === 0 ? null : (
        <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-error)]">{actionError}</p>
      )}
      <ul className="m-0 grid gap-[var(--space-1)] p-0 text-sm text-[var(--color-muted)]">
        <li className="list-none">
          {t("workflowEditor.workflowDeleteProjects", { count: impact.projectCount })}
        </li>
        <li className="list-none">{t("workflowEditor.workflowDeleteLinks", { count: impact.linkCount })}</li>
        <li className="list-none">{t("workflowEditor.workflowDeleteTasks", { count: impact.taskCount })}</li>
      </ul>
      <div className="grid grid-cols-2 gap-[var(--space-2)]">
        <Button className="w-full" disabled={disabled} onClick={onCancel}>
          {t("app.cancel")}
        </Button>
        <Button className="w-full" disabled={disabled} onClick={onConfirm} variant="danger">
          {t("workflowEditor.workflowDeleteConfirm")}
        </Button>
      </div>
    </div>
  );
}

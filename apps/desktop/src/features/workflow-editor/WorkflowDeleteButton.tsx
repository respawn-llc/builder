import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";

import { useWorkflowDeleteLauncher } from "@/shared/workflow-deletion";
import { Button } from "@/ui";

export function WorkflowDeleteButton({
  onDeleted,
  workflowID,
}: Readonly<{
  onDeleted?: (() => void) | undefined;
  workflowID: string;
}>) {
  const { t } = useTranslation();
  const deleteLauncher = useWorkflowDeleteLauncher(workflowID, onDeleted);

  return (
    <>
      {deleteLauncher.dialog}
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

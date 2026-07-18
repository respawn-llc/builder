import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";

import { useWorkflowDeleteLauncher } from "@/shared/workflow-deletion";
import { Button } from "@/ui";

export function WorkflowDeleteButton({ workflowID }: Readonly<{ workflowID: string }>) {
  const { t } = useTranslation();
  const deleteLauncher = useWorkflowDeleteLauncher(workflowID);

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

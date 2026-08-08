import { useTranslation } from "react-i18next";

import { useConnectionSnapshot, useOwnedSidebarRoots } from "@/app-facade";
import { Button, EmptyState } from "@/ui";

export function BoardNoWorkflowState({ projectID }: Readonly<{ projectID: string }>) {
  const { t } = useTranslation();
  const { open } = useOwnedSidebarRoots();
  const connection = useConnectionSnapshot();
  const actionsDisabled = connection.phase !== "connected";
  return (
    <EmptyState
      actions={
        <>
          <Button
            disabled={actionsDisabled}
            onClick={() => {
              open({ kind: "linkWorkflow", mode: "overlay", projectID });
            }}
          >
            {t("workflowLibrary.linkWorkflow")}
          </Button>
          <Button
            disabled={actionsDisabled}
            onClick={() => {
              open({ kind: "workflowCreate", mode: "overlay", projectID });
            }}
            variant="primary"
          >
            {t("workflowLibrary.createWorkflow")}
          </Button>
        </>
      }
      body={t("board.noWorkflowBody")}
      chromePadding
      title={t("board.noWorkflowTitle")}
    />
  );
}

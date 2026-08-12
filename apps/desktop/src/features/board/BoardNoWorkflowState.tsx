import { useTranslation } from "react-i18next";

import {
  useAppNavigation,
  useConnectionSnapshot,
  useOwnedSidebarRoots,
} from "@/app-facade";
import { Button, EmptyState } from "@/ui";

export function BoardNoWorkflowState({ projectID }: Readonly<{ projectID: string }>) {
  const { t } = useTranslation();
  const { open } = useOwnedSidebarRoots();
  const navigation = useAppNavigation();
  const connection = useConnectionSnapshot();
  const actionsDisabled = connection.phase !== "connected";
  return (
    <EmptyState
      actions={
        <>
          <Button
            disabled={actionsDisabled}
            onClick={() => {
              open({
                kind: "linkWorkflow",
                mode: "overlay",
                onCompleted: async (completion) => {
                  if (completion.kind === "created") {
                    await navigation.openWorkflowEditor({
                      projectID,
                      workflowID: completion.workflowID,
                    });
                    return;
                  }
                  await navigation.openProject(projectID, completion.workflowID);
                },
                projectID,
              });
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

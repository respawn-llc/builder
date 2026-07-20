import { type ReactElement } from "react";
import { useTranslation } from "react-i18next";

import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/ui";
import { useWorkflowDeleteLauncher } from "@/shared/workflow-deletion";

export function WorkflowActionsContextMenu({
  children,
  onEdit,
  workflowID,
}: Readonly<{
  children: ReactElement;
  onEdit: () => void;
  workflowID: string;
}>) {
  const { t } = useTranslation();
  const deleteLauncher = useWorkflowDeleteLauncher(workflowID);

  return (
    <>
      {deleteLauncher.dialog}
      <ContextMenu>
        <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
        <ContextMenuContent>
          <ContextMenuItem onSelect={onEdit}>{t("workflowLibrary.edit")}</ContextMenuItem>
          <ContextMenuSeparator />
          <ContextMenuItem
            className="text-[var(--color-error)] data-[highlighted]:text-[var(--color-error)]"
            disabled={deleteLauncher.disabled}
            onSelect={() => {
              void deleteLauncher.openWorkflowDelete();
            }}
          >
            {t("workflowLibrary.delete")}
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
    </>
  );
}

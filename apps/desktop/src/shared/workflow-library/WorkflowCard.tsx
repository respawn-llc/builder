import { useTranslation } from "react-i18next";

import type { WorkflowRecord } from "@/api";
import { HomeListCard } from "@/ui";
import { WorkflowActionsContextMenu } from "./WorkflowActionsContextMenu";

export function WorkflowCard({
  contextActions,
  onOpen,
  workflow,
}: Readonly<{
  contextActions?: WorkflowCardContextActions | undefined;
  onOpen: () => void;
  workflow: WorkflowRecord;
}>) {
  const { t } = useTranslation();
  const card = (
    <HomeListCard ariaLabel={`${workflow.name} rev ${workflow.version.toString()}`} onClick={onOpen}>
      <span className="font-mono text-[0.78rem] text-[var(--color-muted)]">rev {workflow.version}</span>
      <strong>{workflow.name}</strong>
      <span className="truncate text-sm text-[var(--color-muted)]">
        {workflow.description.length > 0 ? workflow.description : t("workflowLibrary.reusableDefinition")}
      </span>
    </HomeListCard>
  );
  if (contextActions === undefined) {
    return card;
  }
  return (
    <WorkflowActionsContextMenu onEdit={contextActions.onEdit} workflowID={workflow.id}>
      {card}
    </WorkflowActionsContextMenu>
  );
}

export type WorkflowCardContextActions = Readonly<{
  onEdit: () => void;
}>;

import { useTranslation } from "react-i18next";
import { Pencil } from "lucide-react";

import type { WorkflowRecord } from "@/api";
import { WorkflowActionsContextMenu } from "./WorkflowActionsContextMenu";

export function WorkflowRow({
  contextActions,
  onOpen,
  workflow,
}: Readonly<{
  contextActions?: WorkflowRowContextActions | undefined;
  onOpen: () => void;
  workflow: WorkflowRecord;
}>) {
  const { t } = useTranslation();
  const row = (
    <div className="group relative flex min-w-0 select-none flex-col gap-[var(--space-1)] rounded-[var(--radius-m)] px-[calc(var(--space-3)/2)] py-[var(--space-1)] text-[var(--color-on-island)] transition-colors hover:bg-[color-mix(in_srgb,var(--color-on-island)_4%,transparent)]">
      <button
        aria-label={workflow.name}
        className="absolute inset-0 z-0 rounded-[var(--radius-m)]"
        onClick={onOpen}
        type="button"
      />
      <div className="pointer-events-none min-w-0 pr-10">
        <strong className="block min-w-0 truncate">{workflow.name}</strong>
        {contextActions === undefined ? null : (
          <button
            aria-label={t("workflowLibrary.editWorkflow", { name: workflow.name })}
            className="pointer-events-auto absolute right-[calc(var(--space-3)/2)] top-[var(--space-1)] z-10 grid h-10 w-10 place-items-center justify-items-end rounded-full text-[var(--color-muted)] hover:text-[var(--color-on-island)]"
            onClick={contextActions.onEdit}
            type="button"
          >
            <Pencil aria-hidden="true" size={14} strokeWidth={1.5} />
          </button>
        )}
      </div>
      <div className="pointer-events-none flex min-w-0 items-center gap-[var(--space-2)] text-left">
        <span className="min-w-0 flex-1 truncate text-xs text-[var(--color-muted)]">
          {workflow.description.length > 0 ? workflow.description : t("workflowLibrary.reusableDefinition")}
        </span>
        <span className="shrink-0 font-mono text-[0.78rem] text-[var(--color-muted)]">
          v{workflow.version}
        </span>
      </div>
    </div>
  );
  if (contextActions === undefined) {
    return row;
  }
  return (
    <WorkflowActionsContextMenu onEdit={contextActions.onEdit} workflowID={workflow.id}>
      {row}
    </WorkflowActionsContextMenu>
  );
}

export type WorkflowRowContextActions = Readonly<{
  onEdit: () => void;
}>;

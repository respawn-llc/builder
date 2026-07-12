import { useTranslation } from "react-i18next";
import { X } from "lucide-react";

import type { WorkflowInspectorInitialFocus, WorkflowInspectorSelection } from "../../app/sidebarContext";
import { Button, IslandSurface } from "../../ui";
import { WorkflowInspectorSidebar } from "./WorkflowInspectorSidebar";

export function WorkflowEditorEmbeddedInspector({
  onClose,
  initialFocus,
  selection,
  workflowID,
}: Readonly<{
  onClose: () => void;
  initialFocus?: WorkflowInspectorInitialFocus | undefined;
  selection: WorkflowInspectorSelection | null;
  workflowID: string;
}>) {
  const { t } = useTranslation();
  if (selection === null) {
    return null;
  }
  const title = workflowInspectorTitle(selection, t);
  return (
    <IslandSurface
      aria-label={title}
      as="aside"
      className="absolute top-[var(--space-2)] right-[var(--space-2)] bottom-[var(--space-2)] z-40 grid w-[min(420px,calc(100%-var(--space-4)))] grid-rows-[auto_1fr] overflow-hidden rounded-[var(--radius-l)] p-0"
      level={3}
    >
      <header className="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-[var(--space-2)] border-b border-[var(--color-outline)] px-[var(--space-3)] py-[var(--space-2)]">
        <Button aria-label={t("app.close")} onClick={onClose} size="icon" variant="ghost">
          <X aria-hidden="true" size={18} strokeWidth={1.5} />
        </Button>
        <h2 className="m-0 truncate text-[1rem] font-bold">{title}</h2>
      </header>
      <div className="min-h-0 overflow-y-auto p-[var(--space-3)]">
        <WorkflowInspectorSidebar
          initialFocus={initialFocus}
          onMissingSelectedNode={onClose}
          selection={selection}
          workflowID={workflowID}
        />
      </div>
    </IslandSurface>
  );
}

function workflowInspectorTitle(
  selection: WorkflowInspectorSelection,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (selection.kind === "workflow") {
    return t("workflowEditor.inspectWorkflow");
  }
  if (selection.kind === "node") {
    return t("workflowEditor.inspectNode");
  }
  if (selection.kind === "group") {
    return t("workflowEditor.inspectGroup");
  }
  return t("workflowEditor.inspectEdge");
}

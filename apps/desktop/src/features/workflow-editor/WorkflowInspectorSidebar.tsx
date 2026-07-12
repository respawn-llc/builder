import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowDefinition } from "../../api";
import type { WorkflowInspectorInitialFocus, WorkflowInspectorSelection } from "../../app/sidebarContext";
import { workflowDefinitionFromDraft } from "./workflowEditorDraft";
import {
  useWorkflowEditorDraftController,
  type WorkflowEditorDraftController,
} from "./workflowEditorDraftBridgeCore";
import { WorkflowDraftInspectorContent } from "./WorkflowDraftInspector";
import { WorkflowInspectorContent } from "./WorkflowReadonlyInspector";
import { useCachedWorkflowDefinition, useCachedWorkflowValidation } from "./workflowInspectorWiring";

export function WorkflowInspectorSidebar({
  onMissingSelectedNode,
  initialFocus,
  selection,
  workflowID,
}: Readonly<{
  onMissingSelectedNode?: (() => void) | undefined;
  initialFocus?: WorkflowInspectorInitialFocus | undefined;
  selection: WorkflowInspectorSelection;
  workflowID: string;
}>) {
  const { t } = useTranslation();
  const controller = useWorkflowEditorDraftController(workflowID);
  const definition = useCachedWorkflowDefinition(workflowID);
  const validation = useCachedWorkflowValidation(workflowID);
  const selectedNodeMissing = selectedNodeNoLongerExists({
    controller,
    definition,
    selection,
  });
  useEffect(() => {
    if (selectedNodeMissing) {
      onMissingSelectedNode?.();
    }
  }, [onMissingSelectedNode, selectedNodeMissing]);
  if (selectedNodeMissing && onMissingSelectedNode !== undefined) {
    return null;
  }
  if (controller !== null) {
    return <WorkflowDraftInspectorContent controller={controller} initialFocus={initialFocus} selection={selection} />;
  }
  if (definition === undefined) {
    return <p className="text-[var(--color-muted)]">{t("workflowEditor.inspectorUnavailable")}</p>;
  }
  return (
    <WorkflowInspectorContent
      definition={definition}
      selection={selection}
      validation={validation ?? { valid: true, errors: [] }}
    />
  );
}

function selectedNodeNoLongerExists({
  controller,
  definition,
  selection,
}: Readonly<{
  controller: WorkflowEditorDraftController | null;
  definition: WorkflowDefinition | undefined;
  selection: WorkflowInspectorSelection;
}>): boolean {
  if (selection.kind !== "node") {
    return false;
  }
  const nodes = controller === null ? definition?.nodes : workflowDefinitionFromDraft(controller.draft).nodes;
  return nodes !== undefined && !nodes.some((node) => node.id === selection.nodeID);
}

import { useTranslation } from "react-i18next";

import type { WorkflowDefinition, WorkflowSelectorApplicabilityReason } from "@/api";
import { DisabledInteractionGuard, SelectField } from "@/ui";
import { DetailSection } from "./WorkflowInspectorPrimitives";
import { type DraftWorkflowEdge } from "./workflowEditorDraft";
import { type WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { ApprovalToggle, FieldSummary } from "./WorkflowInspectorSharedSections";
import {
  EditableEdgeParameters,
  PromptTemplateEditor,
} from "./WorkflowDraftEditableSections";
import {
  edgePromptPlaceholderParameters,
  parameterSummaryFields,
} from "./workflowInspectorWiring";
import type { Translate, derivedEdgeWiring } from "./workflowInspectorWiring";

export function EdgeInvocationSections({
  controller,
  definition,
  derivedEdge,
  edge,
  sourceKind,
  targetKind,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  derivedEdge: ReturnType<typeof derivedEdgeWiring>;
  edge: DraftWorkflowEdge;
  sourceKind: string;
  targetKind: string;
}>) {
  const { t } = useTranslation();
  const promptParameters = edgePromptPlaceholderParameters(definition, edge);
  return (
    <>
      {targetKind === "agent" ? (
        <>
          <EdgeAgentSelectionControls controller={controller} derivedEdge={derivedEdge} edge={edge} />
          <PromptTemplateEditor
            onPromptChange={(promptTemplate) => {
              controller.dispatch({ edgeID: edge.id, promptTemplate, type: "editEdgePrompt" });
            }}
            parameters={promptParameters}
            promptTemplate={edge.promptTemplate}
          />
        </>
      ) : null}
      {sourceKind === "agent" || sourceKind === "script" ? (
        <EditableEdgeParameters
          controller={controller}
          edge={edge}
          protectedParameterVisibility={{
            target_assignee: derivedEdge.assigneeSelectionApplicability.parameterVisible,
            target_thinking: derivedEdge.thinkingSelectionApplicability.parameterVisible,
          }}
        />
      ) : null}
      {sourceKind === "join" && targetKind === "agent" ? (
        <FieldSummary
          fields={parameterSummaryFields(promptParameters)}
          title={t("workflowEditor.joinAggregateParameters")}
        />
      ) : null}
    </>
  );
}

function EdgeAgentSelectionControls({
  controller,
  derivedEdge,
  edge,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  derivedEdge: ReturnType<typeof derivedEdgeWiring>;
  edge: DraftWorkflowEdge;
}>) {
  const { t } = useTranslation();
  const assigneeAvailable = derivedEdge.assigneeSelectionApplicability.available;
  const thinkingAvailable = derivedEdge.thinkingSelectionApplicability.available;
  const assigneeDisabledReason = edgeSelectorDisabledReason(
    derivedEdge.assigneeSelectionApplicability.reason,
    t,
  );
  const thinkingDisabledReason = edgeSelectorDisabledReason(
    derivedEdge.thinkingSelectionApplicability.reason,
    t,
  );
  const assigneeCanReset = edge.assigneeSelection === "previous_node";
  const thinkingCanReset = edge.thinkingSelection === "previous_node";
  return (
    <DetailSection
      title={t("workflowEditor.edgeAssigneeSelection")}
      titleHelp={t("workflowEditor.edgeAssigneeSelectionHelp")}
    >
      <SelectField
        disabled={!assigneeAvailable && !assigneeCanReset}
        disabledReason={assigneeDisabledReason}
        label={t("workflowEditor.edgeAssigneeSelection")}
        onValueChange={(value) => {
          if (value === "configured" || value === "previous_node") {
            controller.dispatch({
              edgeID: edge.id,
              selection: value,
              type: "setEdgeAssigneeSelection",
            });
          }
        }}
        options={[
          {
            label: t("workflowEditor.edgeAssigneeConfigured"),
            value: "configured",
          },
          {
            label: t("workflowEditor.previousNodeAssignee"),
            value: "previous_node",
          },
        ]}
        value={edge.assigneeSelection}
      />
      <DisabledInteractionGuard disabled={!thinkingAvailable && !thinkingCanReset} reason={thinkingDisabledReason}>
        <ApprovalToggle
          checked={edge.thinkingSelection === "previous_node"}
          disabled={!thinkingAvailable && !thinkingCanReset}
          label={t("workflowEditor.previousNodeThinking")}
          labelHelp={t("workflowEditor.previousNodeThinkingHelp")}
          onCheckedChange={(checked) => {
            controller.dispatch({
              edgeID: edge.id,
              selection: checked ? "previous_node" : "configured",
              type: "setEdgeThinkingSelection",
            });
          }}
        />
      </DisabledInteractionGuard>
    </DetailSection>
  );
}

function edgeSelectorDisabledReason(reason: WorkflowSelectorApplicabilityReason, t: Translate): string {
  switch (reason) {
    case "no_callable_roles":
    case "no_thinking_support":
    case "unavailable_configuration":
      return t("workflowEditor.contextSourceUnavailable");
    case "context_source":
    case "topology":
      return t("workflowEditor.edgeControlNotApplicable");
    case "eligible":
    case "sole_callable_role":
    case "no_thinking_levels":
    case "sole_thinking_level":
      return "";
  }
  return t("workflowEditor.contextSourceUnavailable");
}

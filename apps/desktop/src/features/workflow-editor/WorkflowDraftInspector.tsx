import { useTranslation } from "react-i18next";

import type { WorkflowDefinition, WorkflowValidation } from "../../api";
import type { WorkflowInspectorInitialFocus, WorkflowInspectorSelection } from "../../app/sidebarContext";
import {
  DisabledInteractionGuard,
  identifierInputAttributes,
  SelectField,
  TextArea,
  TextInput,
  TooltipProvider,
} from "../../ui";
import {
  DetailRow,
  DetailSection,
  InspectorStack,
  MissingEntity,
  ValidationDetails,
} from "./WorkflowInspectorPrimitives";
import { WorkflowEdgeRouteGraphic } from "./WorkflowEdgeRouteGraphic";
import { transitionGroupByID, transitionGroupIsFanOut } from "./workflowInspectorModel";
import {
  workflowDefinitionFromDraft,
  type DraftWorkflowEdge,
  type DraftWorkflowNode,
} from "./workflowEditorDraft";
import { type WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { ApprovalToggle, Bindings, FieldSummary } from "./WorkflowInspectorSharedSections";
import {
  EditableEdgeParameters,
  EditableJoinProviders,
  PromptTemplateEditor,
} from "./WorkflowDraftEditableSections";
import { GroupDetails } from "./WorkflowReadonlyInspector";
import {
  contextModeOptions,
  contextSourceFromSelectValue,
  contextSourceOptions,
  contextSourceSelectValue,
  derivedEdgeWiring,
  derivedNodeWiring,
  edgeDetails,
  edgePromptPlaceholderParameters,
  emptyWorkflowValidation,
  immediateContextSource,
  parameterSummaryFields,
  workflowCompletionModeOptions,
  useWorkflowAssigneeOptions,
} from "./workflowInspectorWiring";
import { ScriptNodeDraftDetails } from "./WorkflowScriptNodeDraftDetails";

export function WorkflowDraftInspectorContent({
  controller,
  initialFocus,
  selection,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  initialFocus?: WorkflowInspectorInitialFocus | undefined;
  selection: WorkflowInspectorSelection;
}>) {
  const definition = {
    ...workflowDefinitionFromDraft(controller.draft),
    derivedWiring: controller.derivedWiring,
  };
  const validation = controller.dirty.graphDirty
    ? (controller.draftValidation ?? emptyWorkflowValidation)
    : (controller.draftValidation ?? controller.executionValidation ?? emptyWorkflowValidation);
  if (selection.kind === "workflow") {
    return <WorkflowDraftDetails controller={controller} />;
  }
  if (selection.kind === "node") {
    return (
      <WorkflowDraftNodeDetails
        controller={controller}
        definition={definition}
        nodeID={selection.nodeID}
        validation={validation}
      />
    );
  }
  if (selection.kind === "group") {
    const group = definition.nodeGroups.find((item) => item.id === selection.groupID);
    return group === undefined ? (
      <MissingEntity entityID={selection.groupID} />
    ) : (
      <GroupDetails definition={definition} group={group} validation={validation} />
    );
  }
  const edge = controller.draft.edges.find((item) => item.id === selection.edgeID);
  return edge === undefined ? (
    <MissingEntity entityID={selection.edgeID} />
  ) : (
    <EdgeDraftDetails
      controller={controller}
      definition={definition}
      edge={edge}
      initialFocus={initialFocus}
      key={edge.id}
      validation={validation}
    />
  );
}

function WorkflowDraftNodeDetails({
  controller,
  definition,
  nodeID,
  validation,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  nodeID: string;
  validation: WorkflowValidation;
}>) {
  const node = controller.draft.nodes.find((item) => item.id === nodeID);
  if (node === undefined) {
    return <MissingEntity entityID={nodeID} />;
  }
  if (node.kind === "agent") {
    return (
      <AgentNodeDraftDetails
        controller={controller}
        definition={definition}
        node={node}
        validation={validation}
      />
    );
  }
  if (node.kind === "script") {
    return (
      <ScriptNodeDraftDetails
        controller={controller}
        definition={definition}
        node={node}
        validation={validation}
      />
    );
  }
  if (node.kind === "join") {
    return (
      <JoinNodeDraftDetails
        controller={controller}
        definition={definition}
        node={node}
        validation={validation}
      />
    );
  }
  return <FixedNodeDraftDetails controller={controller} node={node} validation={validation} />;
}

function EdgeDraftDetails({
  controller,
  definition,
  edge,
  initialFocus,
  validation,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  edge: DraftWorkflowEdge;
  initialFocus?: WorkflowInspectorInitialFocus | undefined;
  validation: WorkflowValidation;
}>) {
  const { t } = useTranslation();
  const details = edgeDetails(definition, edge, validation);
  const derivedEdge = derivedEdgeWiring(definition, edge.id);
  const transitionGroup = transitionGroupByID(definition, edge.transitionGroupID);
  const fanOutTransition = transitionGroupIsFanOut(definition, edge.transitionGroupID);
  const startEdge = details.sourceKind === "start";
  const targetAgent = details.targetKind === "agent";
  const continuationAvailable = details.sourceKind === "agent" || details.targetKind === "agent";
  const disabledReason = t("workflowEditor.edgeControlNotApplicable");
  const contextModeDisabled = startEdge;
  const requiresApprovalDisabled = startEdge;
  return (
    <InspectorStack>
      <DetailSection
        hideTitle
        leading={
          <WorkflowEdgeRouteGraphic
            contextMode={edge.contextMode}
            hasError={details.hasErrors}
            sourceLabel={details.sourceLabel}
            targetLabel={details.targetLabel}
          />
        }
        title={t("workflowEditor.route")}
      >
        <TextInput
          autoFocus={initialFocus === "firstEditableControl"}
          label={t("workflowEditor.transitionText")}
          onChange={(event) => {
            controller.dispatch({
              input: { edgeID: edge.id, transitionName: event.target.value },
              type: "editEdgeRoute",
            });
          }}
          value={transitionGroup?.name ?? ""}
        />
        <TextArea
          label={t("workflowEditor.transitionDescription")}
          labelHelp={t("workflowEditor.transitionDescriptionHelp")}
          onChange={(event) => {
            controller.dispatch({
              input: { edgeID: edge.id, transitionDescription: event.target.value },
              type: "editEdgeRoute",
            });
          }}
          value={transitionGroup?.description ?? ""}
        />
        <TextInput
          {...identifierInputAttributes}
          label={t("workflowEditor.key")}
          labelHelp={t("workflowEditor.transitionKeyHelp")}
          onChange={(event) => {
            controller.dispatch({
              input: { edgeID: edge.id, transitionID: event.target.value.replaceAll("\n", " ") },
              type: "editEdgeRoute",
            });
          }}
          value={details.transitionID}
        />
        {fanOutTransition ? (
          <TextInput
            {...identifierInputAttributes}
            label={t("workflowEditor.branchKey")}
            onChange={(event) => {
              controller.dispatch({
                input: { edgeID: edge.id, edgeKey: event.target.value.replaceAll("\n", " ") },
                type: "editEdgeRoute",
              });
            }}
            value={edge.key}
          />
        ) : null}
        <TooltipProvider delayDuration={0}>
          {targetAgent ? (
            <>
              <DisabledInteractionGuard disabled={contextModeDisabled} reason={disabledReason}>
                <SelectField
                  disabled={contextModeDisabled}
                  label={t("workflowEditor.contextMode")}
                  labelHelp={t("workflowEditor.contextModeHelp")}
                  onValueChange={(value) => {
                    if (value !== "new_session" && !continuationAvailable) {
                      return;
                    }
                    controller.dispatch({
                      input: {
                        contextMode: value,
                        contextSource: value === "new_session" ? immediateContextSource : edge.contextSource,
                        edgeID: edge.id,
                      },
                      type: "editEdgeRoute",
                    });
                  }}
                  options={contextModeOptions(t, !continuationAvailable)}
                  value={edge.contextMode}
                />
              </DisabledInteractionGuard>
              <SelectField
                label={t("workflowEditor.contextSource")}
                labelHelp={t("workflowEditor.contextSourceHelp")}
                onValueChange={(value) => {
                  controller.dispatch({
                    input: {
                      contextSource: contextSourceFromSelectValue(definition, value),
                      edgeID: edge.id,
                    },
                    type: "editEdgeRoute",
                  });
                }}
                options={contextSourceOptions(definition, edge, t)}
                value={contextSourceSelectValue(definition, edge)}
              />
            </>
          ) : null}
          <DisabledInteractionGuard disabled={requiresApprovalDisabled} reason={disabledReason}>
            <ApprovalToggle
              checked={edge.requiresApproval}
              disabled={requiresApprovalDisabled}
              label={t("workflowEditor.requiresApproval")}
              labelHelp={t("workflowEditor.requiresApprovalHelp")}
              onCheckedChange={(checked) => {
                controller.dispatch({
                  input: { edgeID: edge.id, requiresApproval: checked },
                  type: "editEdgeRoute",
                });
              }}
            />
          </DisabledInteractionGuard>
        </TooltipProvider>
      </DetailSection>
      <EdgeInvocationSections
        controller={controller}
        definition={definition}
        edge={edge}
        sourceKind={details.sourceKind}
        targetKind={details.targetKind}
      />
      <DerivedEdgeSections derivedEdge={derivedEdge} />
      <ValidationDetails errors={details.directErrors} title={t("workflowEditor.edgeErrors")} />
      <ValidationDetails errors={details.groupErrors} title={t("workflowEditor.transitionGroupErrors")} />
    </InspectorStack>
  );
}

function EdgeInvocationSections({
  controller,
  definition,
  edge,
  sourceKind,
  targetKind,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  edge: DraftWorkflowEdge;
  sourceKind: string;
  targetKind: string;
}>) {
  const { t } = useTranslation();
  const promptParameters = edgePromptPlaceholderParameters(definition, edge);
  return (
    <>
      {targetKind === "agent" ? (
        <PromptTemplateEditor
          onPromptChange={(promptTemplate) => {
            controller.dispatch({ edgeID: edge.id, promptTemplate, type: "editEdgePrompt" });
          }}
          parameters={promptParameters}
          promptTemplate={edge.promptTemplate}
        />
      ) : null}
      {sourceKind === "agent" || sourceKind === "script" ? (
        <EditableEdgeParameters controller={controller} edge={edge} />
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

function DerivedEdgeSections({
  derivedEdge,
}: Readonly<{ derivedEdge: ReturnType<typeof derivedEdgeWiring> }>) {
  const { t } = useTranslation();
  return (
    <>
      {derivedEdge.inputBindings.length === 0 ? null : <Bindings bindings={derivedEdge.inputBindings} />}
      {derivedEdge.requiredProvisionFields.length === 0 ? null : (
        <FieldSummary
          fields={derivedEdge.requiredProvisionFields}
          title={t("workflowEditor.derivedProvisionRequirements")}
        />
      )}
      {derivedEdge.requiredProviderFields.length === 0 ? null : (
        <FieldSummary
          fields={derivedEdge.requiredProviderFields}
          title={t("workflowEditor.providerRequirements")}
        />
      )}
    </>
  );
}

function WorkflowDraftDetails({ controller }: Readonly<{ controller: WorkflowEditorDraftController }>) {
  const { t } = useTranslation();
  return (
    <InspectorStack>
      <DetailSection title={t("workflowEditor.workflowSettings")}>
        <TextInput
          label={t("workflowLibrary.name")}
          onChange={(event) => {
            controller.dispatch({
              description: controller.draft.workflow.description,
              name: event.target.value,
              type: "editWorkflowMetadata",
            });
          }}
          value={controller.draft.workflow.name}
        />
        <TextArea
          label={t("workflowLibrary.description")}
          onChange={(event) => {
            controller.dispatch({
              description: event.target.value,
              name: controller.draft.workflow.name,
              type: "editWorkflowMetadata",
            });
          }}
          value={controller.draft.workflow.description}
        />
      </DetailSection>
      <DetailSection title={t("workflowEditor.inspectorOverview")}>
        <DetailRow label={t("workflowEditor.version")} value={controller.draft.workflow.version.toString()} />
        <DetailRow label={t("workflowEditor.nodeCount")} value={controller.draft.nodes.length.toString()} />
        <DetailRow label={t("workflowEditor.edgeCount")} value={controller.draft.edges.length.toString()} />
        <DetailRow
          label={t("workflowEditor.groupCount")}
          value={controller.draft.nodeGroups.length.toString()}
        />
      </DetailSection>
      <ValidationDetails errors={controller.draftValidation?.errors ?? []} />
    </InspectorStack>
  );
}

function AgentNodeDraftDetails({
  controller,
  definition,
  node,
  validation,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  node: DraftWorkflowNode;
  validation: WorkflowValidation;
}>) {
  const { t } = useTranslation();
  const assigneeOptions = useWorkflowAssigneeOptions(definition);
  const errors = validation.errors.filter(
    (error) => error.nodeID === node.id || error.relatedIDs.includes(node.id),
  );
  return (
    <InspectorStack>
      <DetailSection>
        <TextInput
          label={t("workflowEditor.displayName")}
          onChange={(event) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { name: event.target.value },
              type: "editAgentNode",
            });
          }}
          value={node.name}
        />
        <TextInput
          {...identifierInputAttributes}
          label={t("workflowEditor.key")}
          onChange={(event) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { key: event.target.value },
              type: "editAgentNode",
            });
          }}
          value={node.key}
        />
        <SelectField
          label={t("workflowEditor.assignee")}
          labelHelp={t("workflowEditor.assigneeHelp")}
          onValueChange={(value) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { subagentRole: value },
              type: "editAgentNode",
            });
          }}
          options={assigneeOptions}
          placeholder={t("workflowEditor.selectAssignee")}
          value={node.subagentRole}
        />
        <SelectField
          label={t("workflowEditor.completionMode")}
          labelHelp={t("workflowEditor.completionModeHelp")}
          onValueChange={(value) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { completionMode: value },
              type: "editAgentNode",
            });
          }}
          options={workflowCompletionModeOptions(t)}
          value={node.completionMode}
        />
      </DetailSection>
      <FieldSummary
        fields={derivedNodeWiring(definition, node.id).possibleProvisionFields}
        title={t("workflowEditor.outputs")}
      />
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

function FixedNodeDraftDetails({
  controller,
  node,
  validation,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  node: DraftWorkflowNode;
  validation: WorkflowValidation;
}>) {
  const { t } = useTranslation();
  const errors = validation.errors.filter(
    (error) => error.nodeID === node.id || error.relatedIDs.includes(node.id),
  );
  return (
    <InspectorStack>
      <DetailSection>
        <TextInput
          label={t("workflowEditor.displayName")}
          onChange={(event) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { name: event.target.value },
              type: "editNodeIdentity",
            });
          }}
          value={node.name}
        />
        <TextInput
          {...identifierInputAttributes}
          label={t("workflowEditor.key")}
          onChange={(event) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { key: event.target.value },
              type: "editNodeIdentity",
            });
          }}
          value={node.key}
        />
      </DetailSection>
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

function JoinNodeDraftDetails({
  controller,
  definition,
  node,
  validation,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  node: DraftWorkflowNode;
  validation: WorkflowValidation;
}>) {
  const { t } = useTranslation();
  const errors = validation.errors.filter(
    (error) => error.nodeID === node.id || error.relatedIDs.includes(node.id),
  );
  return (
    <InspectorStack>
      <DetailSection title={t("workflowEditor.inspectorIdentity")}>
        <DetailRow help={t("workflowEditor.keyHelp")} label={t("workflowEditor.key")} mono value={node.key} />
      </DetailSection>
      <EditableJoinProviders controller={controller} definition={definition} node={node} />
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

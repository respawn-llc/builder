import { useTranslation } from "react-i18next";

import type {
  WorkflowDefinition,
  WorkflowEdge,
  WorkflowNode,
  WorkflowNodeGroup,
  WorkflowValidation,
} from "../../api";
import type { WorkflowInspectorSelection } from "../../app/sidebarContext";
import {
  DetailRow,
  DetailSection,
  InspectorStack,
  MissingEntity,
  ValidationDetails,
} from "./WorkflowInspectorPrimitives";
import { WorkflowEdgeRouteGraphic } from "./WorkflowEdgeRouteGraphic";
import { fallbackLabel, transitionGroupIsFanOut } from "./workflowInspectorModel";
import { Bindings, FieldSummary, JoinProviders, PromptPreview } from "./WorkflowInspectorSharedSections";
import {
  derivedEdgeWiring,
  derivedNodeWiring,
  edgeDetails,
  edgePromptPlaceholderParameters,
  formatContextModeLabel,
  formatContextSourceLabel,
  parameterSummaryFields,
} from "./workflowInspectorWiring";

export function WorkflowInspectorContent({
  definition,
  selection,
  validation,
}: Readonly<{
  definition: WorkflowDefinition;
  selection: WorkflowInspectorSelection;
  validation: WorkflowValidation;
}>) {
  if (selection.kind === "workflow") {
    return <WorkflowDetails definition={definition} validation={validation} />;
  }
  if (selection.kind === "node") {
    const node = definition.nodes.find((item) => item.id === selection.nodeID);
    return node === undefined ? (
      <MissingEntity entityID={selection.nodeID} />
    ) : (
      <NodeDetails definition={definition} node={node} validation={validation} />
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
  const edge = definition.edges.find((item) => item.id === selection.edgeID);
  return edge === undefined ? (
    <MissingEntity entityID={selection.edgeID} />
  ) : (
    <EdgeDetails definition={definition} edge={edge} validation={validation} />
  );
}

function WorkflowDetails({
  definition,
  validation,
}: Readonly<{ definition: WorkflowDefinition; validation: WorkflowValidation }>) {
  const { t } = useTranslation();
  return (
    <InspectorStack>
      <DetailSection title={t("workflowEditor.inspectorOverview")}>
        <DetailRow
          label={t("workflowEditor.version")}
          value={definition.workflow.version.toString()}
        />
        <DetailRow label={t("workflowEditor.nodeCount")} value={definition.nodes.length.toString()} />
        <DetailRow label={t("workflowEditor.edgeCount")} value={definition.edges.length.toString()} />
        <DetailRow label={t("workflowEditor.groupCount")} value={definition.nodeGroups.length.toString()} />
      </DetailSection>
      {definition.workflow.description.length > 0 ? (
        <DetailSection title={t("workflowEditor.description")}>
          <p className="m-0 text-sm text-[var(--color-on-island)]">{definition.workflow.description}</p>
        </DetailSection>
      ) : null}
      <ValidationDetails errors={validation.errors} />
    </InspectorStack>
  );
}

export function NodeDetails({
  definition,
  node,
  validation,
}: Readonly<{ definition: WorkflowDefinition; node: WorkflowNode; validation: WorkflowValidation }>) {
  const { t } = useTranslation();
  const errors = validation.errors.filter(
    (error) => error.nodeID === node.id || error.relatedIDs.includes(node.id),
  );
  const derivedNode = derivedNodeWiring(definition, node.id);
  return (
    <InspectorStack>
      <DetailSection>
        <DetailRow help={t("workflowEditor.keyHelp")} label={t("workflowEditor.key")} mono value={node.key} />
        {node.kind === "agent" ? (
          <DetailRow
            help={t("workflowEditor.assigneeHelp")}
            label={t("workflowEditor.assignee")}
            value={fallbackLabel(t("workflowEditor.none"), node.subagentRole)}
          />
        ) : null}
        {node.kind === "script" ? (
          <DetailRow
            help={t("workflowEditor.scriptPathHelp")}
            label={t("workflowEditor.scriptPath")}
            mono
            value={fallbackLabel(t("workflowEditor.none"), node.scriptPath ?? "")}
          />
        ) : null}
      </DetailSection>
      {node.kind === "agent" || node.kind === "script" ? (
        <FieldSummary fields={derivedNode.possibleProvisionFields} title={t("workflowEditor.outputs")} />
      ) : null}
      {node.kind === "join" ? (
        <>
          <FieldSummary fields={derivedNode.joinOutputFields} title={t("workflowEditor.joinRequiredInputs")} />
          <JoinProviders definition={definition} node={node} />
        </>
      ) : null}
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

export function GroupDetails({
  definition,
  group,
  validation,
}: Readonly<{ definition: WorkflowDefinition; group: WorkflowNodeGroup; validation: WorkflowValidation }>) {
  const { t } = useTranslation();
  const members = definition.nodes.filter((node) => node.groupID === group.id);
  const errors = validation.errors.filter((error) => error.relatedIDs.includes(group.id));
  return (
    <InspectorStack>
      <DetailSection title={t("workflowEditor.inspectorIdentity")}>
        <DetailRow label={t("workflowEditor.key")} mono value={group.key} />
        <DetailRow label={t("workflowEditor.id")} mono value={group.id} />
      </DetailSection>
      <DetailSection title={t("workflowEditor.members")}>
        {members.length === 0 ? (
          <p className="m-0 text-sm text-[var(--color-muted)]">{t("workflowEditor.emptyGroup")}</p>
        ) : (
          <ul className="m-0 grid gap-[var(--space-1)] p-0">
            {members.map((node) => (
              <li className="list-none text-sm" key={node.id}>
                {fallbackLabel(node.key, node.name)}{" "}
                <span className="font-mono text-[var(--color-muted)]">({node.kind})</span>
              </li>
            ))}
          </ul>
        )}
      </DetailSection>
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

function EdgeDetails({
  definition,
  edge,
  validation,
}: Readonly<{ definition: WorkflowDefinition; edge: WorkflowEdge; validation: WorkflowValidation }>) {
  const { t } = useTranslation();
  const details = edgeDetails(definition, edge, validation);
  const derivedEdge = derivedEdgeWiring(definition, edge.id);
  const promptParameters = edgePromptPlaceholderParameters(definition, edge);
  const fanOutTransition = transitionGroupIsFanOut(definition, edge.transitionGroupID);
  const targetAgent = details.targetKind === "agent";
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
        <DetailRow
          help={t("workflowEditor.transitionKeyHelp")}
          label={t("workflowEditor.key")}
          mono
          value={details.transitionID}
        />
        {fanOutTransition ? <DetailRow label={t("workflowEditor.branchKey")} mono value={edge.key} /> : null}
        <DetailRow label={t("workflowEditor.transitionText")} value={details.transitionGroupLabel} />
        {details.transitionDescription.length > 0 ? (
          <DetailRow
            help={t("workflowEditor.transitionDescriptionHelp")}
            label={t("workflowEditor.transitionDescription")}
            value={details.transitionDescription}
          />
        ) : null}
        {targetAgent ? (
          <>
            <DetailRow
              help={t("workflowEditor.contextModeHelp")}
              label={t("workflowEditor.contextMode")}
              value={formatContextModeLabel(edge.contextMode, t)}
            />
            <DetailRow
              help={t("workflowEditor.contextSourceHelp")}
              label={t("workflowEditor.contextSource")}
              value={formatContextSourceLabel(edge, t)}
            />
          </>
        ) : null}
        <DetailRow
          help={t("workflowEditor.requiresApprovalHelp")}
          label={t("workflowEditor.requiresApproval")}
          value={edge.requiresApproval ? t("workflowEditor.required") : t("workflowEditor.none")}
        />
      </DetailSection>
      {details.targetKind === "agent" ? (
        <PromptPreview help={t("workflowEditor.promptHelp")} prompt={edge.promptTemplate} />
      ) : null}
      {details.sourceKind === "agent" ? (
        <FieldSummary
          fields={parameterSummaryFields(edge.parameters)}
          title={t("workflowEditor.parameters")}
          titleHelp={t("workflowEditor.parametersHelp")}
        />
      ) : null}
      {details.sourceKind === "join" && details.targetKind === "agent" ? (
        <FieldSummary
          fields={parameterSummaryFields(promptParameters)}
          title={t("workflowEditor.joinAggregateParameters")}
        />
      ) : null}
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
      <ValidationDetails errors={details.directErrors} title={t("workflowEditor.edgeErrors")} />
      <ValidationDetails errors={details.groupErrors} title={t("workflowEditor.transitionGroupErrors")} />
    </InspectorStack>
  );
}

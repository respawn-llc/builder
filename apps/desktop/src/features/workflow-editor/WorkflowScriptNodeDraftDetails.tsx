import { FileSearch } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { WorkflowDefinition, WorkflowEdge, WorkflowValidation } from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppServices } from "../../app/useAppServices";
import { Button, identifierInputAttributes, showStatusToast, TextInput } from "../../ui";
import { writeClipboardText } from "../../ui/clipboard";
import { DetailSection, InspectorStack, ValidationDetails } from "./WorkflowInspectorPrimitives";
import type { DraftWorkflowNode } from "./workflowEditorDraft";
import { useWorkflowScriptPathValidationQuery } from "./workflowEditorQueries";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import { FieldSummary } from "./WorkflowInspectorSharedSections";
import { derivedNodeWiring, type Translate } from "./workflowInspectorWiring";

export function ScriptNodeDraftDetails({
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
  const { nativeBridge } = useAppServices();
  const errors = validation.errors.filter(
    (error) => error.nodeID === node.id || error.relatedIDs.includes(node.id),
  );
  const scriptPathValidation = useWorkflowScriptPathValidationQuery(
    controller.workflowID,
    node.id,
    node.scriptPath ?? "",
  );
  const scriptPathErrors = scriptPathValidation.data?.errors ?? [];
  const scriptPathFieldErrorMessages =
    scriptPathValidation.error === null
      ? scriptPathErrors.filter((error) => error.blocksContext).map((error) => error.message)
      : [errorMessage(scriptPathValidation.error)];
  const scriptPathFieldErrors =
    scriptPathFieldErrorMessages.length === 0 ? undefined : scriptPathFieldErrorMessages;
  const scriptPathDiagnostics = scriptPathErrors.filter((error) => !error.blocksContext);
  const canPickScript = nativeBridge.capabilities.files.select;
  async function pickScriptPath(): Promise<void> {
    const selection = await nativeBridge.files.selectFile({ title: t("workflowEditor.selectScriptPath") });
    if (selection === null) {
      return;
    }
    controller.dispatch({
      nodeID: node.id,
      patch: { scriptPath: selection.path },
      type: "editScriptNode",
    });
  }
  return (
    <InspectorStack>
      <DetailSection>
        <TextInput
          label={t("workflowEditor.displayName")}
          onChange={(event) => {
            controller.dispatch({
              nodeID: node.id,
              patch: { name: event.target.value },
              type: "editScriptNode",
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
              type: "editScriptNode",
            });
          }}
          value={node.key}
        />
        <TextInput
          error={scriptPathFieldErrors}
          label={t("workflowEditor.scriptPath")}
          labelHelp={t("workflowEditor.scriptPathHelp")}
          onChange={(event) => {
            const value = event.target.value;
            controller.dispatch({
              nodeID: node.id,
              patch: { scriptPath: value.length === 0 ? null : value },
              type: "editScriptNode",
            });
          }}
          trailingControl={
            canPickScript ? (
              <Button
                aria-label={t("workflowEditor.selectScriptPath")}
                className="h-8 w-8"
                onClick={() => {
                  void pickScriptPath().catch((cause: unknown) => {
                    showStatusToast({
                      id: "workflow-script-path-picker-failed",
                      title: t("workflowEditor.selectScriptPathFailed"),
                      body: errorMessage(cause),
                      tone: "danger",
                    });
                  });
                }}
                title={t("workflowEditor.selectScriptPath")}
                type="button"
                variant="ghost"
                size="icon"
              >
                <FileSearch aria-hidden="true" size={16} strokeWidth={1.8} />
              </Button>
            ) : undefined
          }
          value={node.scriptPath ?? ""}
        />
      </DetailSection>
      <FieldSummary
        fields={derivedNodeWiring(definition, node.id).possibleProvisionFields}
        title={t("workflowEditor.outputs")}
      />
      <ScriptStdoutExample definition={definition} node={node} />
      <ScriptPathDiagnostics errors={scriptPathDiagnostics} />
      <ValidationDetails errors={errors} />
    </InspectorStack>
  );
}

function ScriptPathDiagnostics({ errors }: Readonly<{ errors: WorkflowValidation["errors"] }>) {
  const { t } = useTranslation();
  if (errors.length === 0) {
    return null;
  }
  return (
    <DetailSection title={t("workflowEditor.scriptPathDiagnostics")}>
      <ul className="m-0 grid list-disc gap-[var(--space-1)] pl-[1.1rem] text-sm leading-snug">
        {errors.map((error) => (
          <li className="pl-[2px] marker:text-[var(--color-warning)]" key={`${error.code}:${error.nodeID}`}>
            {error.message}
          </li>
        ))}
      </ul>
    </DetailSection>
  );
}

function ScriptStdoutExample({
  definition,
  node,
}: Readonly<{
  definition: WorkflowDefinition;
  node: DraftWorkflowNode;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const example = scriptStdoutExample(definition, node.id, t);
  return (
    <DetailSection
      title={t("workflowEditor.scriptStdoutExample")}
      titleHelp={t("workflowEditor.scriptStdoutExampleHelp")}
    >
      <pre className="m-0 overflow-auto rounded-[var(--radius-m)] border border-[var(--color-border)] bg-[var(--color-surface)] p-[var(--space-3)] text-xs leading-relaxed">
        <code>{example}</code>
      </pre>
      <Button
        onClick={() => {
          void writeClipboardText(example, nativeBridge)
            .then(() => {
              showStatusToast({
                id: "workflow-script-stdout-example-copied",
                title: t("workflowEditor.scriptStdoutExampleCopied"),
                tone: "success",
              });
            })
            .catch((cause: unknown) => {
              showStatusToast({
                id: "workflow-script-stdout-example-copy-failed",
                title: t("workflowEditor.scriptStdoutExampleCopyFailed"),
                body: errorMessage(cause),
                tone: "danger",
              });
            });
        }}
        type="button"
        variant="secondary"
      >
        {t("workflowEditor.copyScriptStdoutExample")}
      </Button>
    </DetailSection>
  );
}

function scriptStdoutExample(definition: WorkflowDefinition, nodeID: string, translate: Translate): string {
  const groups = definition.transitionGroups.filter((group) => group.sourceNodeID === nodeID);
  const selectedGroup = groups.at(0);
  const selectedTransitionGroupID = selectedGroup?.id ?? "";
  const payload: Record<string, string | null> = {
    commentary: translate("workflowEditor.scriptStdoutExampleCommentary"),
  };
  if (groups.length > 1 && selectedGroup !== undefined) {
    payload.transition = selectedGroup.transitionID;
  }
  for (const parameter of scriptCompletionParameters(
    definition.edges,
    groups.map((group) => group.id),
    selectedTransitionGroupID,
  )) {
    payload[parameter.key] = parameter.selected
      ? translate("workflowEditor.scriptStdoutExampleParameterValue", { parameter: parameter.key })
      : null;
  }
  return JSON.stringify(payload, null, 2);
}

function scriptCompletionParameters(
  edges: readonly WorkflowEdge[],
  transitionGroupIDs: readonly string[],
  selectedTransitionGroupID: string,
): readonly { key: string; selected: boolean }[] {
  const selectedGroups = new Set(transitionGroupIDs);
  const selectedParameterKeys = new Set<string>();
  const out: { key: string; selected: boolean }[] = [];
  const knownKeys = new Set<string>();
  for (const edge of edges) {
    if (!selectedGroups.has(edge.transitionGroupID)) {
      continue;
    }
    const selected = edge.transitionGroupID === selectedTransitionGroupID;
    for (const parameter of edge.parameters) {
      if (selected) {
        selectedParameterKeys.add(parameter.key);
      }
      if (!knownKeys.has(parameter.key)) {
        knownKeys.add(parameter.key);
        out.push({ key: parameter.key, selected: false });
      }
    }
  }
  return out.map((parameter) => ({ ...parameter, selected: selectedParameterKeys.has(parameter.key) }));
}

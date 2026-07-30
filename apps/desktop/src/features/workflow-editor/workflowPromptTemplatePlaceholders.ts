import type { WorkflowParameter } from "@/api";
import { isWorkflowModelKeyValid } from "./workflowEditorGraphKeys";

export type PromptTemplatePlaceholderTone = "muted" | "primary";
export type PromptTemplatePlaceholderInfoTopic = "node_output" | "transition_parameter";

export type PromptTemplatePlaceholder =
  | Readonly<{
      kind: "insert";
      label: string;
      tone: PromptTemplatePlaceholderTone;
      value: string;
    }>
  | Readonly<{
      kind: "info";
      label: string;
      tone: PromptTemplatePlaceholderTone;
      topic: PromptTemplatePlaceholderInfoTopic;
    }>;

export const transitionKeyedParameterPlaceholderLabel = "{{.Params.<transition_key>.<parameter>}}";

export const transitionKeyedParameterPlaceholderExample = "{{.Params.planning.plan_file_location}}";

export const priorNodeOutputPlaceholderLabel = "{{.Nodes.<node_key>.<field>}}";

export const priorNodeOutputPlaceholderExample = "{{.Nodes.planning.plan_file_location}}";

// Keep this in sync with server/workflowrunner/starter.go nodePromptTemplateData.
export const builtInPromptTemplatePlaceholderNames = [
  "TaskId",
  "TaskShortId",
  "TaskTitle",
  "TaskBody",
  "NodeId",
  "NodeKey",
  "NodeDisplayName",
] as const;

export const commentaryPromptTemplatePlaceholder = {
  kind: "insert" as const,
  label: ".Params.commentary",
  tone: "muted" as const,
  value: "{{.Params.commentary}}",
};

export function workflowPromptTemplatePlaceholders(
  parameters: readonly Pick<WorkflowParameter, "key">[],
): readonly PromptTemplatePlaceholder[] {
  const seen = new Set<string>();
  const parameterPlaceholders = parameters.flatMap((parameter) => {
    const parameterKey = parameter.key.trim();
    if (!isWorkflowModelKeyValid(parameterKey)) {
      return [];
    }
    const value = `{{.Params.${parameterKey}}}`;
    if (seen.has(value)) {
      return [];
    }
    seen.add(value);
    return [{ kind: "insert" as const, label: `.Params.${parameterKey}`, tone: "primary" as const, value }];
  });
  return [
    ...parameterPlaceholders,
    {
      kind: "info" as const,
      label: transitionKeyedParameterPlaceholderLabel,
      tone: "primary" as const,
      topic: "transition_parameter" as const,
    },
    {
      kind: "info" as const,
      label: priorNodeOutputPlaceholderLabel,
      tone: "primary" as const,
      topic: "node_output" as const,
    },
    ...builtInPromptTemplatePlaceholderNames.map((name) => ({
      kind: "insert" as const,
      label: `.${name}`,
      tone: "muted" as const,
      value: `{{.${name}}}`,
    })),
    commentaryPromptTemplatePlaceholder,
  ];
}

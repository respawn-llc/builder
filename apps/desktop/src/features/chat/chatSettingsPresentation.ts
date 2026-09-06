import type { TFunction } from "i18next";

import type { ChatSettings, ChatSettingsAutoCompaction, ChatSettingsEditability } from "@/api";

import type { ReadyChatSettings } from "./useChatSettings";

export function settingsPresentation(feature: ReadyChatSettings) {
  if (feature.kind === "ready-session") return feature.settings;
  const selection = feature.initialSettings;
  const choice = feature.catalog.choices.find((entry) => entry.agent.role === selection.agentRole);
  if (choice === undefined) throw new Error("Selected Agent is not in the New Chat catalog.");
  return {
    selectedAgent: choice.agent,
    agentEditability: { kind: "editable" },
    supervisor: { ...choice.supervisor, value: selection.supervisor },
    thinking:
      choice.thinking.kind === "unsupported"
        ? choice.thinking
        : { ...choice.thinking, value: requiredValue(selection.thinking) },
    fast:
      choice.fast.kind === "unsupported"
        ? choice.fast
        : { ...choice.fast, value: requiredValue(selection.fast) },
    questions: { ...choice.questions, enabled: selection.questionsEnabled },
    autoCompaction:
      choice.autoCompaction.policy === "optional"
        ? {
            ...choice.autoCompaction,
            stored: selection.autoCompactionEnabled,
            effective: selection.autoCompactionEnabled,
          }
        : choice.autoCompaction,
  } satisfies Pick<
    ChatSettings,
    "selectedAgent" | "agentEditability" | "supervisor" | "thinking" | "fast" | "questions" | "autoCompaction"
  >;
}

function requiredValue<Value>(value: Value | null): Value {
  if (value === null) throw new Error("A supported New Chat setting has no selected value.");
  return value;
}

export function settingsDisabledReason(
  t: TFunction,
  editability: ChatSettingsEditability,
  disconnected: boolean,
  autoCompactionPolicy?: ChatSettingsAutoCompaction["policy"],
): string | undefined {
  if (disconnected) return t("common.readOnly");
  if (autoCompactionPolicy === "required") return t("chatSettings.required");
  switch (editability.kind) {
    case "editable":
      return undefined;
    case "workflow_lock":
      return t("chatSettings.workflowLock");
    case "caching_lock":
      return t("chatSettings.cachingLock");
    case "policy_disabled":
      return t("chatSettings.policyDisabled");
  }
}

import type { TFunction } from "i18next";

import {
  ChatOperationError,
  errorMessage,
  type ChatError,
  type ChatSettings,
  type ChatSettingsAutoCompaction,
  type ChatSettingsEditability,
} from "@/api";

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

export function settingsOperationFailureMessage(t: TFunction, error: unknown): string {
  if (!(error instanceof ChatOperationError)) return errorMessage(error);
  return typedSettingsOperationFailureMessage(t, error.detail);
}

function typedSettingsOperationFailureMessage(t: TFunction, detail: ChatError): string {
  switch (detail.kind) {
    case "session_not_found":
      return t("chatSettings.errors.sessionNotFound");
    case "workspace_not_registered":
      return t("chatSettings.errors.workspaceNotRegistered");
    case "agent_preparation":
      return agentPreparationFailureMessage(t, detail);
    case "auth_required":
      return t("chatSettings.errors.authRequired");
    case "server_not_ready":
      return t("chatSettings.errors.serverNotReady");
    case "runtime_unavailable":
      return t("chatSettings.errors.runtimeUnavailable");
    case "internal_failure":
      return internalFailureMessage(t, detail);
    case "unknown":
      return t("chatSettings.errors.unknown", { code: detail.code });
  }
}

function agentPreparationFailureMessage(
  t: TFunction,
  detail: Extract<ChatError, { kind: "agent_preparation" }>,
): string {
  switch (detail.category) {
    case "invalid_configuration":
      return t("chatSettings.errors.agentInvalidConfiguration", { agent: detail.agent });
    case "provider_unavailable":
      return t("chatSettings.errors.agentProviderUnavailable", { agent: detail.agent });
    case "internal_preparation":
      return t("chatSettings.errors.agentInternalPreparation", { agent: detail.agent });
  }
}

function internalFailureMessage(
  t: TFunction,
  detail: Extract<ChatError, { kind: "internal_failure" }>,
): string {
  return [
    t("chatSettings.errors.internalFailure"),
    detail.operation === null ? null : t("chatSettings.errors.operation", { operation: detail.operation }),
    detail.cause === null ? null : t("chatSettings.errors.cause", { cause: detail.cause }),
  ]
    .filter((line): line is string => line !== null)
    .join("\n");
}

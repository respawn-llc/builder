import { create } from "@app/server-api-contract";
import {
  AutoCompactionPolicy,
  ChatSettingsService,
  Editability,
  MutationRejectionReason,
  ThinkingKind,
  type AgentChoice,
  type InitialChatSettings as WireInitialChatSettings,
  type NewChatAgentChoice,
  type Settings,
  type SessionFacts,
  type MutationOperation,
} from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";
import {
  CompactionMode,
  type Context,
} from "@app/server-api-contract/gen/kent/api/chat_context/chat_context_pb";

import { requireProjectAttachment } from "./chatAttachment";
import { requireChatSuccess } from "./chatErrors";
import { requireChatProjectTarget, requireChatSessionID } from "./chatTarget";
import { supervisorFromWire, supervisorToWire } from "./chatSupervisor";
import type { ChatApi, ChatContext, InitialChatSettings } from "./chatTypes";
import type {
  ChatSettings,
  ChatSettingsControls,
  ChatSettingsEditability,
  ChatSettingsSessionFacts,
  ChatSettingsMutation,
  ChatSettingsRejection,
} from "./chatSettingsTypes";
import { ContractError } from "./errors";

import type { DescriptorRpcTransport } from "./transport";

export function createChatSettingsApi(
  transport: DescriptorRpcTransport,
): Pick<ChatApi, "getSettings" | "mutateSettings"> {
  return {
    async mutateSettings(target, operation) {
      const sessionID = requireChatSessionID(target);
      const method = ChatSettingsService.method.mutate;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: () =>
          create(method.input, {
            session: { sessionId: sessionID },
            operation: { operation: mutationOperation(operation) },
          }),
      });
      requireProjectAttachment(call.attachment, target);
      const value = requireChatSuccess(method, call.result);
      const result = required(value.result).outcome;
      if (result.case === undefined) throw new ContractError("Chat Settings mutation outcome is invalid.");
      return {
        result:
          result.case === "applied"
            ? { kind: "applied", changed: result.value.changed }
            : { kind: "rejected", reason: rejection(result.value.reason) },
        settings: settings(required(value.settings)),
        session: sessionFacts(required(value.session), sessionID),
        context: context(required(value.context)),
      };
    },
    async getSettings(target) {
      if (target.kind === "session") requireChatSessionID(target);
      else requireChatProjectTarget(target);
      const method = ChatSettingsService.method.read;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: (attachment) =>
          create(method.input, {
            target:
              target.kind === "new_chat"
                ? {
                    case: "newChat",
                    value: { projectId: attachment.projectID, workspaceId: attachment.workspaceID },
                  }
                : { case: "session", value: { sessionId: target.sessionID } },
          }),
      });
      requireProjectAttachment(call.attachment, target);
      const response = requireChatSuccess(method, call.result);
      if (target.kind === "session" && response.target.case === "session") {
        return {
          kind: "session",
          settings: settings(required(response.target.value.settings)),
          session: sessionFacts(required(response.target.value.session), target.sessionID),
        };
      }
      if (target.kind !== "new_chat" || response.target.case !== "newChat") {
        throw new ContractError("Chat Settings response target does not match the request.");
      }
      return {
        kind: "new_chat",
        initialSettings: initialSettings(required(response.target.value.initialSettings)),
        catalog: {
          choices: response.target.value.choices.map((choice) => ({
            agent: agentChoice(required(choice.agent)),
            baseline: initialSettings(required(choice.baseline)),
            ...controls(choice),
          })),
        },
      };
    },
  };
}

function rejection(value: MutationRejectionReason): ChatSettingsRejection {
  switch (value) {
    case MutationRejectionReason.AGENT_LOCKED:
      return "agent_locked";
    case MutationRejectionReason.AGENT_UNAVAILABLE:
      return "agent_unavailable";
    case MutationRejectionReason.THINKING_UNAVAILABLE:
      return "thinking_unavailable";
    case MutationRejectionReason.FAST_UNAVAILABLE:
      return "fast_unavailable";
    case MutationRejectionReason.AUTO_COMPACTION_POLICY_LOCKED:
      return "auto_compaction_policy_locked";
    case MutationRejectionReason.UNSPECIFIED:
      throw new ContractError("Chat Settings rejection reason is invalid.");
  }
}

function mutationOperation(operation: ChatSettingsMutation): MutationOperation["operation"] {
  switch (operation.kind) {
    case "agent":
      return { case: "agentRole", value: operation.role };
    case "supervisor":
      return { case: "supervisor", value: supervisorToWire(operation.value) };
    case "thinking":
      return { case: "thinking", value: operation.value };
    case "fast":
      return { case: "fastEnabled", value: operation.enabled };
    case "questions":
      return { case: "questionsEnabled", value: operation.enabled };
    case "auto_compaction":
      return { case: "autoCompactionEnabled", value: operation.enabled };
  }
}

function context(value: Context): ChatContext {
  let compactionMode: string;
  switch (value.compactionMode) {
    case CompactionMode.DISABLED:
      compactionMode = "disabled";
      break;
    case CompactionMode.LOCAL:
      compactionMode = "local";
      break;
    case CompactionMode.PROVIDER_NATIVE:
      compactionMode = "provider_native";
      break;
    case CompactionMode.UNSPECIFIED:
      throw new ContractError("Chat Context compaction mode is invalid.");
  }
  return {
    contextWindowTokens: Number(value.contextWindowTokens),
    usedTokens: Number(value.usedTokens),
    remainingTokens: Number(value.remainingTokens),
    automaticThresholdTokens: Number(value.automaticThresholdTokens),
    autoCompactionEnabled: value.autoCompactionEnabled,
    compactionMode,
    completedCompactionCount: Number(value.completedCompactionCount),
    compactionRunning: value.compactionRunning,
    manualCompactAvailable: value.manualCompactAvailable,
  };
}

function settings(value: Settings): ChatSettings {
  const agent = required(value.selectedAgent);
  return {
    selectedAgent: { role: agent.role, model: agent.model, thinking: agent.thinking },
    agentChoices: value.agentChoices.map(agentChoice),
    agentEditability: editability(value.agentEditability),
    agentLocked: value.agentLocked,
    workflowLocked: value.workflowLocked,
    cachingLocked: value.cachingLocked,
    ...controls(value),
  };
}

function sessionFacts(value: SessionFacts, sessionID: string): ChatSettingsSessionFacts {
  if (value.sessionId !== sessionID) {
    throw new ContractError("Chat Settings response Session does not match the request.");
  }
  return {
    sessionID: value.sessionId,
    previousSessionID: value.previousSessionId ?? null,
    task: value.taskId === undefined ? null : { taskID: value.taskId, shortID: required(value.taskShortId) },
  };
}

function required<T>(value: T | undefined): T {
  if (value === undefined) throw new ContractError("Chat Settings response omitted a required field.");
  return value;
}

function initialSettings(value: WireInitialChatSettings): InitialChatSettings {
  return {
    agentRole: value.agentRole,
    supervisor: supervisorFromWire(value.supervisor),
    thinking: value.thinking ?? null,
    fast: value.fast ?? null,
    questionsEnabled: required(value.questionsEnabled),
    autoCompactionEnabled: required(value.autoCompactionEnabled),
  };
}

function agentChoice(value: AgentChoice) {
  return {
    role: value.role,
    model: value.model,
    thinking: value.thinking,
    tools: value.tools,
    customSystemPrompt: value.customSystemPrompt,
    customCapabilities: value.customCapabilities,
    agentCallable: value.agentCallable,
  };
}

function editability(value: Editability): ChatSettingsEditability {
  switch (value) {
    case Editability.EDITABLE:
      return { kind: "editable" };
    case Editability.WORKFLOW_LOCK:
      return { kind: "workflow_lock" };
    case Editability.CACHING_LOCK:
      return { kind: "caching_lock" };
    case Editability.POLICY_DISABLED:
      return { kind: "policy_disabled" };
    case Editability.UNSPECIFIED:
      throw new ContractError("Chat Settings editability is invalid.");
  }
}

function controls(value: Settings | NewChatAgentChoice): ChatSettingsControls {
  const supervisorValue = required(value.supervisor);
  const questions = required(value.questions);
  return {
    supervisor: {
      value: supervisorFromWire(supervisorValue.value),
      baseline: supervisorFromWire(supervisorValue.baseline),
      editability: editability(supervisorValue.editability),
    },
    thinking: thinking(value.thinking),
    fast:
      value.fast === undefined
        ? { kind: "unsupported" }
        : {
            kind: "supported",
            value: value.fast.value,
            editability: editability(value.fast.editability),
          },
    questions: {
      capable: questions.capable,
      enabled: questions.enabled,
      editability: editability(questions.editability),
    },
    autoCompaction: autoCompaction(required(value.autoCompaction)),
  };
}

function thinking(value: Settings["thinking"]): ChatSettingsControls["thinking"] {
  if (value === undefined) return { kind: "unsupported" };
  const shared = {
    value: value.value,
    baselineValue: value.baselineValue,
    editability: editability(value.editability),
  };
  switch (value.kind) {
    case ThinkingKind.ENUMERATED:
      return { kind: "enumerated", ...shared, values: value.values };
    case ThinkingKind.CUSTOM:
      return { kind: "custom", ...shared };
    case ThinkingKind.UNSPECIFIED:
      throw new ContractError("Chat Settings Thinking kind is invalid.");
  }
}

function autoCompaction(
  value: NonNullable<Settings["autoCompaction"]>,
): ChatSettingsControls["autoCompaction"] {
  switch (value.policy) {
    case AutoCompactionPolicy.OPTIONAL:
      return {
        policy: "optional",
        stored: value.stored,
        effective: value.effective,
        editability: { kind: "editable" },
      };
    case AutoCompactionPolicy.REQUIRED:
      return {
        policy: "required",
        stored: value.stored,
        effective: true,
        editability: { kind: "workflow_lock" },
      };
    case AutoCompactionPolicy.DISABLED:
      return {
        policy: "disabled",
        stored: value.stored,
        effective: false,
        editability: { kind: "policy_disabled" },
      };
    case AutoCompactionPolicy.UNSPECIFIED:
      throw new ContractError("Chat Settings Auto-compaction policy is invalid.");
  }
}

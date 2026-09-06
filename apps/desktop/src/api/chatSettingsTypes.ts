import type { ChatContext, InitialChatSettings } from "./chatTypes";

export type ChatSettingsEditability =
  | Readonly<{ kind: "editable" }>
  | Readonly<{ kind: "workflow_lock" }>
  | Readonly<{ kind: "caching_lock" }>
  | Readonly<{ kind: "policy_disabled" }>;

export type ChatSettingsAgent = Readonly<{
  role: string;
  model: string;
  thinking: string;
}>;
export type ChatSettingsAgentChoice = ChatSettingsAgent &
  Readonly<{
    tools: readonly string[];
    customSystemPrompt: boolean;
    customCapabilities: boolean;
    agentCallable: boolean;
  }>;
export type ChatSettingsSupervisor = Readonly<{
  value: InitialChatSettings["supervisor"];
  baseline: InitialChatSettings["supervisor"];
  editability: ChatSettingsEditability;
}>;
type SupportedThinking = Readonly<{
  value: string;
  baselineValue: string;
  editability: ChatSettingsEditability;
}>;
export type ChatSettingsThinking =
  | Readonly<{ kind: "unsupported" }>
  | (SupportedThinking & Readonly<{ kind: "enumerated"; values: readonly string[] }>)
  | (SupportedThinking & Readonly<{ kind: "custom" }>);
export type ChatSettingsFast =
  | Readonly<{ kind: "unsupported" }>
  | Readonly<{ kind: "supported"; value: boolean; editability: ChatSettingsEditability }>;
export type ChatSettingsAutoCompaction =
  | Readonly<{
      policy: "optional";
      stored: boolean;
      effective: boolean;
      editability: Readonly<{ kind: "editable" }>;
    }>
  | Readonly<{
      policy: "required";
      stored: boolean;
      effective: true;
      editability: Readonly<{ kind: "workflow_lock" }>;
    }>
  | Readonly<{
      policy: "disabled";
      stored: boolean;
      effective: false;
      editability: Readonly<{ kind: "policy_disabled" }>;
    }>;
export type ChatSettingsControls = Readonly<{
  supervisor: ChatSettingsSupervisor;
  thinking: ChatSettingsThinking;
  fast: ChatSettingsFast;
  questions: Readonly<{ capable: boolean; enabled: boolean; editability: ChatSettingsEditability }>;
  autoCompaction: ChatSettingsAutoCompaction;
}>;
export type ChatSettings = ChatSettingsControls &
  Readonly<{
    selectedAgent: ChatSettingsAgent;
    agentChoices: readonly ChatSettingsAgentChoice[];
    agentEditability: ChatSettingsEditability;
    agentLocked: boolean;
    workflowLocked: boolean;
    cachingLocked: boolean;
  }>;
export type ChatSettingsSessionFacts = Readonly<{
  sessionID: string;
  previousSessionID: string | null;
  task: Readonly<{ taskID: string; shortID: string }> | null;
}>;
export type NewChatSettingsCatalog = Readonly<{
  choices: readonly (ChatSettingsControls &
    Readonly<{
      agent: ChatSettingsAgentChoice;
      baseline: InitialChatSettings;
    }>)[];
}>;
export type ChatSettingsRead =
  | Readonly<{ kind: "new_chat"; catalog: NewChatSettingsCatalog; initialSettings: InitialChatSettings }>
  | Readonly<{ kind: "session"; settings: ChatSettings; session: ChatSettingsSessionFacts }>;
export type ChatSettingsMutation =
  | Readonly<{ kind: "agent"; role: string }>
  | Readonly<{ kind: "supervisor"; value: InitialChatSettings["supervisor"] }>
  | Readonly<{ kind: "thinking"; value: string }>
  | Readonly<{ kind: "fast"; enabled: boolean }>
  | Readonly<{ kind: "questions"; enabled: boolean }>
  | Readonly<{ kind: "auto_compaction"; enabled: boolean }>;
export type ChatSettingsRejection =
  | "agent_locked"
  | "agent_unavailable"
  | "thinking_unavailable"
  | "fast_unavailable"
  | "auto_compaction_policy_locked";
export type ChatSettingsMutationResponse = Readonly<{
  result:
    | Readonly<{ kind: "applied"; changed: boolean }>
    | Readonly<{ kind: "rejected"; reason: ChatSettingsRejection }>;
  settings: ChatSettings;
  session: ChatSettingsSessionFacts;
  context: ChatContext;
}>;

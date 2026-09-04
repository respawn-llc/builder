import type { ApiSubscription } from "./apiService";
import type { ChatTranscriptMessage, ChatTranscriptPayloadByKind } from "./chatTranscriptSchemas";
export type {
  ChatTranscriptKind,
  ChatTranscriptMessage,
  ChatTranscriptMessageByKind,
  ChatTranscriptPayload,
  ChatTranscriptPayloadByKind,
} from "./chatTranscriptSchemas";

export type ChatWorkspaceSelector = Readonly<{ workspaceID: string } | { workspaceRoot: string }>;
export type ChatProjectTarget = Readonly<{ projectID: string; workspace: ChatWorkspaceSelector }>;
export type ChatSessionTarget = ChatProjectTarget & Readonly<{ sessionID: string }>;
export type ChatContextTarget = ChatProjectTarget & Readonly<{ sessionID?: string }>;
export type ChatSettingsTarget =
  (ChatProjectTarget & Readonly<{ kind: "new_chat" }>) | (ChatSessionTarget & Readonly<{ kind: "session" }>);

export type ChatMainView = Readonly<{
  version: Readonly<{ epoch: string; generation: number; sequence: number }>;
  status: ChatRuntimeStatus;
  sessionID: string;
  sessionName: string | null;
  executionTarget: ChatExecutionTarget;
  activity: ChatRuntimeActivity;
}>;
export type ChatExecutionTarget = Readonly<{
  workspaceID: string;
  workspaceName: string;
  workspaceRoot: string;
  workspaceAvailability: "available" | "missing" | "inaccessible" | "unlinked";
  worktree: Readonly<{ ID: string; Name: string; Root: string; Availability: string }> | null;
  cwdRelpath: string;
  effectiveWorkdir: string;
}>;
export type ChatRuntimeStatus = Readonly<{
  reviewerFrequency: string;
  reviewerEnabled: boolean;
  autoCompactionEnabled: boolean;
  questionsEnabled: boolean;
  fastModeAvailable: boolean;
  fastModeEnabled: boolean;
  conversationFreshness: 0 | 1;
  previousSessionID: string | null;
  parentAgentSessionID: string | null;
  navigationTargetSessionID: string | null;
  lastCommittedAssistantFinalAnswer: string | null;
  thinkingLevel: string;
  compactionMode: string;
  contextUsage: Readonly<{
    usedTokens: number;
    windowTokens: number;
    cacheHitPercent: number;
    hasCacheHitPercentage: boolean;
  }>;
  compactionCount: number;
  goal: Readonly<{
    id: string;
    objective: string;
    status: "active" | "paused" | "complete";
    created_at: string;
    updated_at: string;
    availability: "available" | "agent_capability_missing";
    suspended: boolean;
  }> | null;
  workflowSession: Readonly<{ taskID: string; workflowID: string }> | null;
}>;
export type ChatRuntimeActivity = Readonly<{
  state:
    "unavailable" | "registered_idle" | "starting" | "running" | "awaiting_prompt" | "draining" | "closing";
  activeStep: Readonly<{ runID: string; stepID: string; activeKind: string }> | null;
  reviewer: "inactive" | "invoking" | "addressing_feedback";
  queueAccepting: boolean;
  diagnosticRecovery: boolean;
}>;
export type ChatContext = Readonly<{
  contextWindowTokens: number;
  usedTokens: number;
  remainingTokens: number;
  automaticThresholdTokens: number;
  autoCompactionEnabled: boolean;
  compactionMode: string;
  completedCompactionCount: number;
  compactionRunning: boolean;
  manualCompactAvailable: boolean;
}>;
export type ChatSettings = Readonly<{
  selectedAgent: Readonly<{ role: string; model: string; thinking: string }>;
  agentChoices: readonly Readonly<{
    role: string;
    model: string;
    thinking: string;
    tools: readonly string[];
    customSystemPrompt: boolean;
    customCapabilities: boolean;
    agentCallable: boolean;
  }>[];
  agentEditability: string;
  supervisor: Readonly<{
    value: "off" | "edits" | "all";
    baseline: "off" | "edits" | "all";
    editability: string;
  }>;
  thinking: Readonly<{
    kind: "enumerated" | "custom";
    value: string;
    baselineValue: string;
    values: readonly string[];
    editability: string;
  }> | null;
  fast: Readonly<{ value: boolean; editability: string }> | null;
  questions: Readonly<{ capable: boolean; enabled: boolean; editability: string }>;
  autoCompaction: Readonly<{
    policy: "optional" | "required" | "disabled";
    stored: boolean;
    effective: boolean;
    editability: string;
  }>;
  agentLocked: boolean;
  workflowLocked: boolean;
  cachingLocked: boolean;
  session: Readonly<{ sessionID: string; previousSessionID: string | null; taskID: string | null }> | null;
}>;
export type ChatTranscriptPage = Readonly<{
  sessionID: string;
  sessionName: string | null;
  conversationFreshness: number;
  olderCursor: number | null;
  hasMoreAbove: boolean;
  newerCursor: number | null;
  hasMoreBelow: boolean;
  latestRollbackCandidate: Readonly<{ user_message_seq: number; candidate_page_end_byte: number }> | null;
  entries: readonly ChatTranscriptCommittedRow[];
}>;
export type ChatTranscriptCommittedRow = ChatTranscriptPayloadByKind["committed_row"];
export type ChatTranscriptCompletion = Readonly<{
  code: number;
  message: string;
  reason: "subscriber_overflow" | "contract_violation" | null;
}>;
export type ChatTranscriptHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: ChatTranscriptMessage): void;
  onComplete(completion: ChatTranscriptCompletion): void;
  onError(error: Error): void;
}>;
export type ChatRuntimeAttachment = Readonly<{ sessionID: string; generation: number }>;
export type ChatRuntimeRelease = Readonly<{ released: boolean; active: boolean }>;
export type ChatApi = Readonly<{
  getMainView(target: ChatSessionTarget): Promise<ChatMainView>;
  getContext(target: ChatContextTarget): Promise<ChatContext>;
  getSettings(target: ChatSettingsTarget): Promise<ChatSettings>;
  getTranscriptPage(
    target: ChatSessionTarget,
    cursor?: Readonly<{ direction: "older" | "newer"; value: number }>,
  ): Promise<ChatTranscriptPage>;
  activateRuntime(target: ChatSessionTarget): Promise<ChatRuntimeAttachment>;
  releaseRuntime(attachment: ChatRuntimeAttachment): Promise<ChatRuntimeRelease>;
  subscribeTranscript(target: ChatSessionTarget, handler: ChatTranscriptHandler): ApiSubscription;
}>;

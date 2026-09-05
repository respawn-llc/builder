import type { ApiSubscription } from "./apiService";
import type {
  ChatSettingsRead,
  ChatSettingsMutation,
  ChatSettingsMutationResponse,
} from "./chatSettingsTypes";
export type { ChatSettings } from "./chatSettingsTypes";
import type { ChatTranscriptMessage, ChatTranscriptPayloadByKind } from "./chatTranscriptSchemas";
import type {
  CompactionRequestID,
  PendingWork,
  PendingWorkIdentity,
  PendingWorkItemID,
  PendingWorkRestoration,
} from "./pendingWork";
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
export type InitialChatSettings = Readonly<{
  agentRole: string;
  supervisor: "off" | "edits" | "all";
  thinking: string | null;
  fast: boolean | null;
  questionsEnabled: boolean;
  autoCompactionEnabled: boolean;
}>;
export type ChatMutationTarget =
  | (ChatSessionTarget & Readonly<{ kind: "session" }>)
  | (ChatProjectTarget & Readonly<{ kind: "new_chat"; initialSettings: InitialChatSettings }>);
export type ChatActivation =
  | Readonly<{ kind: "text"; text: string }>
  | Readonly<{
      kind: "command";
      catalogIdentity: string;
      token: string;
      separatorWhitespace: string;
      arguments: string;
    }>;
export type ChatInputMutationResult = Readonly<{
  sessionID: string;
  outcome:
    | Readonly<{
        kind: "accepted";
        queueItemID: PendingWorkItemID;
        diagnostic: ChatAcceptedDiagnostic | null;
      }>
    | Readonly<{
        kind: "not_accepted";
        reason: ChatNotAcceptedReason;
      }>;
}>;
export type ChatNotAcceptedReason =
  | Readonly<{ kind: "canceled" }>
  | Readonly<{ kind: "runtime_unavailable" }>
  | Readonly<{ kind: "pending_work_capacity" }>
  | Readonly<{ kind: "prompt_catalog_read"; command: string | null }>
  | Readonly<{ kind: "prompt_command_not_found"; command: string }>
  | Readonly<{ kind: "prompt_command_read"; command: string }>
  | Readonly<{ kind: "too_soon" }>
  | Readonly<{ kind: "disabled" }>
  | Readonly<{ kind: "active" }>
  | Readonly<{ kind: "internal_failure"; operation: string | null; cause: string | null }>;
export type ChatAcceptedDiagnostic = Readonly<{
  kind: "prompt_history_failure" | "internal_failure";
  operation: string | null;
  cause: string | null;
}>;
export type ChatCompactionInvocation = Readonly<{
  token: "/compact";
  separatorWhitespace: string;
  rawGuidance: string;
}>;
export type ChatCompactionResult = Readonly<{
  sessionID: string;
  outcome:
    | Readonly<{
        kind: "accepted";
        requestID: CompactionRequestID;
        diagnostic: ChatAcceptedDiagnostic | null;
      }>
    | Readonly<{ kind: "not_accepted"; reason: ChatNotAcceptedReason }>;
}>;
export type ChatForkEditInput = Readonly<{ rollbackTargetID: string; initialInput: string }>;

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
  steer(target: ChatMutationTarget, activation: ChatActivation): Promise<ChatInputMutationResult>;
  queue(target: ChatMutationTarget, activation: ChatActivation): Promise<ChatInputMutationResult>;
  compact(target: ChatMutationTarget, invocation: ChatCompactionInvocation): Promise<ChatCompactionResult>;
  stop(target: ChatSessionTarget): Promise<"stopped" | "idle">;
  forkEdit(target: ChatSessionTarget, input: ChatForkEditInput): Promise<string>;
  listPendingWork(target: ChatSessionTarget): Promise<PendingWork>;
  removePendingWork(target: ChatSessionTarget, itemID: PendingWorkIdentity): Promise<PendingWorkRestoration>;
  getMainView(target: ChatSessionTarget): Promise<ChatMainView>;
  getContext(target: ChatContextTarget): Promise<ChatContext>;
  getSettings(target: ChatSettingsTarget): Promise<ChatSettingsRead>;
  mutateSettings(
    target: ChatSessionTarget,
    operation: ChatSettingsMutation,
  ): Promise<ChatSettingsMutationResponse>;
  getTranscriptPage(
    target: ChatSessionTarget,
    cursor?: Readonly<{ direction: "older" | "newer"; value: number }>,
  ): Promise<ChatTranscriptPage>;
  activateRuntime(target: ChatSessionTarget): Promise<ChatRuntimeAttachment>;
  releaseRuntime(attachment: ChatRuntimeAttachment): Promise<ChatRuntimeRelease>;
  subscribeTranscript(target: ChatSessionTarget, handler: ChatTranscriptHandler): ApiSubscription;
}>;

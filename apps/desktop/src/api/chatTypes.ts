import type { ApiSubscription } from "./apiService";
import type {
  ChatCommittedRowWire,
  ChatDiagnosticWire,
  ChatReasoningIdentityWire,
  ChatRuntimeReadModelUpdateWire,
  ChatSessionIdentityWire,
  ChatSessionStatusWire,
  ChatToolMetaWire,
} from "./chatSchemas";
import type { ChatBackgroundActivityWire, ChatHydrationWire, ChatPromptWire } from "./chatHydrationSchemas";

type DeepReadonly<Value> = Value extends (...args: never[]) => unknown
  ? Value
  : Value extends readonly (infer Element)[]
    ? readonly DeepReadonly<Element>[]
    : Value extends object
      ? { readonly [Key in keyof Value]: DeepReadonly<Value[Key]> }
      : Value;

export type ChatWorkspaceSelector = Readonly<{ workspaceID: string } | { workspaceRoot: string }>;
export type ChatProjectTarget = Readonly<{ projectID: string; workspace: ChatWorkspaceSelector }>;
export type ChatSessionTarget = ChatProjectTarget & Readonly<{ sessionID: string }>;
export type ChatContextTarget = ChatProjectTarget & Readonly<{ sessionID?: string }>;
export type ChatSettingsTarget =
  (ChatProjectTarget & Readonly<{ kind: "lazy" }>) | (ChatSessionTarget & Readonly<{ kind: "session" }>);

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
export type ChatTranscriptCommittedRow = DeepReadonly<ChatCommittedRowWire>;
export type ChatTranscriptKind =
  | "hydration"
  | "committed_row"
  | "assistant_delta"
  | "assistant_stream_abort"
  | "thinking_status_update"
  | "reasoning_trace_update"
  | "reasoning_trace_reset"
  | "tool_start"
  | "tool_abort"
  | "user_message_flushed"
  | "queued_message_state"
  | "pending_work_changed"
  | "pending_work_restored"
  | "session_setting_feedback"
  | "human_input_interrupted"
  | "step_state"
  | "runtime_read_model_update"
  | "session_status"
  | "session_identity"
  | "compaction_status"
  | "context_usage"
  | "goal_status"
  | "background_activity"
  | "prompt"
  | "worktree_transition_outcome"
  | "operational_diagnostic"
  | "live_run_finished";
export interface ChatTranscriptPayloadByKind {
  hydration: DeepReadonly<ChatHydrationWire>;
  committed_row: ChatTranscriptCommittedRow;
  assistant_delta: Readonly<{
    StepID: string;
    StreamID: string;
    Delta: string;
    Phase: "commentary" | "final_answer";
  }>;
  assistant_stream_abort: Readonly<{
    StepID: string;
    StreamID: string;
    Reason: "interrupted" | "failed" | "superseded";
    Diagnostic?: DeepReadonly<ChatDiagnosticWire> | null | undefined;
  }>;
  thinking_status_update: Readonly<{ StepID: string; Text: string }>;
  reasoning_trace_update: Readonly<{
    StepID: string;
    Identity: DeepReadonly<ChatReasoningIdentityWire>;
    CompactText: string;
    Text: string;
  }>;
  reasoning_trace_reset: Readonly<{ StepID: string }>;
  tool_start: Readonly<{
    StepID: string;
    ToolCallID: string;
    ToolName: string;
    Presentation?: DeepReadonly<ChatToolMetaWire> | null | undefined;
  }>;
  tool_abort: Readonly<{
    StepID: string;
    ToolCallID: string;
    Reason: "canceled" | "failed";
    Diagnostic?: DeepReadonly<ChatDiagnosticWire> | null | undefined;
  }>;
  user_message_flushed: Readonly<{ StepID?: string | null | undefined }>;
  queued_message_state: Readonly<{
    QueueItemID: string;
    Status: "accepted" | "submitted" | "failed" | "discarded";
    FailureReason?: "closing" | "terminal_workflow_completion" | "runtime_unavailable" | null | undefined;
    Text?: string | null | undefined;
  }>;
  pending_work_changed: Readonly<Record<string, never>>;
  pending_work_restored: Readonly<{
    Restoration: Readonly<{ ItemID: string; Kind: string; CanonicalInput: string }>;
  }>;
  session_setting_feedback: Readonly<{
    Kind: "session_name" | "thinking" | "fast_mode" | "supervisor" | "questions" | "auto_compaction";
    Changed: boolean;
    SessionName?: string | null | undefined;
    Thinking?: string | null | undefined;
    FastMode?: boolean | null | undefined;
    Supervisor?: string | null | undefined;
    Questions?: boolean | null | undefined;
    AutoCompaction?: boolean | null | undefined;
  }>;
  human_input_interrupted: Readonly<{ Items: readonly Readonly<{ QueueItemID: string; Text: string }>[] }>;
  step_state: Readonly<{
    RunID: string;
    StepID: string;
    Lifecycle: "started" | "finished";
    ActiveKind: string;
    Status: "running" | "completed" | "interrupted" | "failed";
  }>;
  runtime_read_model_update: DeepReadonly<ChatRuntimeReadModelUpdateWire>;
  session_status: DeepReadonly<ChatSessionStatusWire>;
  session_identity: DeepReadonly<ChatSessionIdentityWire>;
  compaction_status: Readonly<{
    StepID: string;
    RequestID?: string | null | undefined;
    State: "started" | "completed" | "failed";
    Mode: "auto" | "handoff" | "manual" | "workflow_post_completion";
    Count: number;
    Diagnostic?: DeepReadonly<ChatDiagnosticWire> | null | undefined;
  }>;
  context_usage: Readonly<{
    UsedTokens: number;
    WindowTokens: number;
    CacheHitPercent?: number | null | undefined;
  }>;
  goal_status: Readonly<{
    Goal: Readonly<{
      id: string;
      objective: string;
      status: "active" | "paused" | "complete";
      created_at: string;
      updated_at: string;
      Suspended: boolean;
    }> | null;
    Availability: "available" | "agent_capability_missing" | null;
  }>;
  background_activity: DeepReadonly<ChatBackgroundActivityWire>;
  prompt: DeepReadonly<ChatPromptWire>;
  worktree_transition_outcome: Readonly<{
    OperationID: string;
    Transition: "enter" | "leave" | "delete";
    State: "completed" | "failed";
    Failure?: DeepReadonly<ChatDiagnosticWire> | null | undefined;
    SelectorError?:
      | Readonly<{
          kind: 1 | 2 | 3;
          input: string;
          candidates?:
            | readonly Readonly<{
                variant: 1 | 2 | 3;
                selector: string;
                branch_name?: string | undefined;
                display_name?: string | undefined;
                fallback_identity: string;
              }>[]
            | undefined;
        }>
      | null
      | undefined;
    DeletePrecondition?:
      | Readonly<{
          kind: "clean" | "dirty" | "unknown";
          dirty_file_count?: number | undefined;
          unknown_cause?: string | undefined;
        }>
      | null
      | undefined;
  }>;
  operational_diagnostic: Readonly<{
    Code:
      | "sleep_guard_failed"
      | "prompt_history_persist_failed"
      | "context_facts_persist_failed"
      | "in_flight_clear_failed"
      | "provider_turn_state_invalid";
    StepID?: string | null | undefined;
    Detail: string;
  }>;
  live_run_finished: Readonly<{
    Status: "completed" | "interrupted" | "failed";
    ResultKind: "assistant_final_answer" | "no_final_answer";
    NoFinalReason: string;
    WorkPerformed: boolean;
    FinalAnswer?: string | null | undefined;
    Failure?: string | null | undefined;
    StartedAt: string;
    FinishedAt: string;
  }>;
}
export type ChatTranscriptPayload = ChatTranscriptPayloadByKind[ChatTranscriptKind];
export type ChatTranscriptMessageByKind = {
  [Kind in ChatTranscriptKind]: Readonly<{
    sequence: number;
    kind: Kind;
    payload: ChatTranscriptPayloadByKind[Kind];
  }>;
}[ChatTranscriptKind];
export type ChatTranscriptMessage = ChatTranscriptMessageByKind;
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

import { z } from "zod";

import { jsonValueSchema } from "./json";

export const nonBlank = z.string().trim().min(1);
export const record = z.record(z.string(), jsonValueSchema);
export const optionalNullable = <T extends z.ZodType>(schema: T) => schema.nullable().optional();
export const timestamp = z.iso.datetime({ offset: true });
export const identifier = z.string().trim().min(1);
export const optionalIdentifier = optionalNullable(identifier);
export const projectAttachmentSchema = z.object({
  projectID: nonBlank,
  workspaceID: nonBlank,
  workspaceRoot: nonBlank,
});
export const runtimeWorktreeSchema = z
  .object({
    ID: identifier,
    Name: identifier,
    Root: identifier,
    Availability: z.enum(["available", "missing", "inaccessible"]),
  })
  .strict();
export const executionTargetSchema = z
  .object({
    WorkspaceID: z.string(),
    WorkspaceName: z.string(),
    WorkspaceRoot: nonBlank,
    WorkspaceAvailability: z.enum(["available", "missing", "inaccessible", "unlinked"]),
    Worktree: runtimeWorktreeSchema.nullable(),
    CwdRelpath: z.string(),
    EffectiveWorkdir: nonBlank,
  })
  .superRefine((target, context) => {
    if (target.WorkspaceAvailability !== "unlinked" && target.WorkspaceID.trim().length === 0) {
      context.addIssue({
        code: "custom",
        path: ["WorkspaceID"],
        message: "linked execution targets require a Workspace ID",
      });
    }
  });
export const runtimeContextUsageSchema = z
  .object({
    UsedTokens: z.number().int().nonnegative(),
    WindowTokens: z.number().int().positive(),
    CacheHitPercent: z.number().int().nonnegative(),
    HasCacheHitPercentage: z.boolean(),
  })
  .strict();
export const goalSchema = z
  .object({
    id: identifier,
    objective: identifier,
    status: z.enum(["active", "paused", "complete"]),
    created_at: timestamp,
    updated_at: timestamp,
  })
  .strict();
export const runtimeStatusSchema = z
  .object({
    ReviewerFrequency: identifier,
    ReviewerEnabled: z.boolean(),
    AutoCompactionEnabled: z.boolean(),
    QuestionsEnabled: z.boolean(),
    FastModeAvailable: z.boolean(),
    FastModeEnabled: z.boolean(),
    ConversationFreshness: z.union([z.literal(0), z.literal(1)]),
    PreviousSessionID: optionalIdentifier,
    ParentAgentSessionID: optionalIdentifier,
    NavigationTargetSessionID: optionalIdentifier,
    LastCommittedAssistantFinalAnswer: optionalNullable(z.string()),
    ThinkingLevel: identifier,
    CompactionMode: identifier,
    ContextUsage: runtimeContextUsageSchema,
    CompactionCount: z.number().int().nonnegative(),
    Goal: z
      .object({
        ...goalSchema.shape,
        Availability: z.enum(["available", "agent_capability_missing"]),
        Suspended: z.boolean(),
      })
      .strict()
      .nullable(),
    WorkflowSession: z.object({ TaskID: identifier, WorkflowID: identifier }).strict().nullable(),
  })
  .strict();
export const runtimeActivitySchema = z
  .object({
    State: z.enum([
      "unavailable",
      "registered_idle",
      "starting",
      "running",
      "awaiting_prompt",
      "draining",
      "closing",
    ]),
    ActiveStep: z
      .object({ RunID: identifier, StepID: identifier, ActiveKind: identifier })
      .strict()
      .nullable(),
    Reviewer: z.enum(["inactive", "invoking", "addressing_feedback"]),
    QueueAccepting: z.boolean(),
    DiagnosticRecovery: z.boolean(),
  })
  .strict();
export const mainViewSchema = z
  .object({
    MainView: z
      .object({
        Version: z.object({
          Epoch: nonBlank,
          Generation: z.number().int().positive(),
          Sequence: z.number().int().positive(),
        }),
        Status: runtimeStatusSchema,
        Session: z
          .object({
            SessionID: nonBlank,
            SessionName: z.string(),
            AgentRole: z.string().nullable(),
            ConversationFreshness: z.union([z.literal(0), z.literal(1)]),
            ExecutionTarget: executionTargetSchema,
          })
          .strict(),
        Activity: runtimeActivitySchema,
      })
      .strict(),
  })
  .strict();
export const contextSchema = z
  .object({
    context: z
      .object({
        context_window_tokens: z.number().int().positive(),
        used_tokens: z.number().int().nonnegative(),
        remaining_tokens: z.number().int(),
        automatic_threshold_tokens: z.number().int().nonnegative(),
        auto_compaction_enabled: z.boolean(),
        compaction_mode: nonBlank,
        completed_compaction_count: z.number().int().nonnegative(),
        compaction_running: z.boolean(),
        manual_compact_available: z.boolean(),
      })
      .strict(),
  })
  .strict();
export const settingsSchema = z
  .object({
    settings: z
      .object({
        selected_agent: z.object({ role: nonBlank, model: nonBlank, thinking: nonBlank }).strict(),
        agent_choices: z.array(
          z
            .object({
              role: nonBlank,
              model: nonBlank,
              thinking: nonBlank,
              tools: z.array(nonBlank),
              custom_system_prompt: z.boolean(),
              custom_capabilities: z.boolean(),
              agent_callable: z.boolean(),
            })
            .strict(),
        ),
        agent_editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
        supervisor: z
          .object({
            value: z.enum(["off", "edits", "all"]),
            baseline: z.enum(["off", "edits", "all"]),
            editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
          })
          .strict(),
        thinking: z
          .object({
            kind: z.enum(["enumerated", "custom"]),
            value: nonBlank,
            baseline_value: nonBlank,
            values: z.array(nonBlank),
            editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
          })
          .strict()
          .nullable()
          .optional(),
        fast: z
          .object({
            value: z.boolean(),
            editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
          })
          .strict()
          .nullable()
          .optional(),
        questions: z
          .object({
            capable: z.boolean(),
            enabled: z.boolean(),
            editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
          })
          .strict(),
        auto_compaction: z
          .object({
            policy: z.enum(["optional", "required", "disabled"]),
            stored: z.boolean(),
            effective: z.boolean(),
            editability: z.enum(["editable", "workflow_lock", "caching_lock", "policy_disabled"]),
          })
          .strict(),
        agent_locked: z.boolean(),
        workflow_locked: z.boolean(),
        caching_locked: z.boolean(),
      })
      .strict(),
    session: z
      .object({
        session_id: nonBlank,
        previous_session_id: nonBlank.nullable().optional(),
        task_id: z.string().nullable().optional(),
      })
      .strict()
      .optional(),
  })
  .strict();
export const locatorSchema = z
  .object({ event_sequence: z.number().int().positive(), row_ordinal: z.number().int().positive() })
  .strict();
export const committedAtSchema = z.number().int().min(-8640000000000000).max(8640000000000000);
export const optionalText = optionalNullable(z.string());
export const diagnosticSchema = z.object({ Code: identifier, Detail: identifier }).strict();
export const userRowSchema = z
  .object({
    StepID: optionalIdentifier,
    Text: identifier,
    CondensedText: optionalText,
    RollbackTargetID: optionalText,
    committed_at_unix_ms: optionalNullable(committedAtSchema),
  })
  .strict();
export const assistantRowSchema = z
  .object({
    StepID: identifier,
    StreamID: optionalIdentifier,
    Text: identifier,
    CondensedText: optionalText,
    Phase: z.enum(["commentary", "final_answer"]),
    committed_at_unix_ms: optionalNullable(committedAtSchema),
  })
  .strict();
export const toolMetaSchema = z
  .object({
    ToolName: z.string(),
    Presentation: z.string(),
    RenderBehavior: z.string(),
    IsShell: z.boolean(),
    UserInitiated: z.boolean(),
    Command: z.string(),
    CompactText: z.string(),
    InlineMeta: z.string(),
    TimeoutLabel: z.string(),
    PatchSummary: z.string(),
    PatchDetail: z.string(),
    PatchRender: optionalNullable(record),
    RenderHint: optionalNullable(
      z
        .object({
          Kind: z.string(),
          Path: z.string(),
          ResultOnly: z.boolean(),
          ShellDialect: z.string(),
        })
        .strict(),
    ),
    Question: z.string(),
    Suggestions: z.array(z.string()),
    RecommendedOptionIndex: z.number().int(),
    OmitSuccessfulResult: z.boolean(),
    RawOutputRequested: z.boolean(),
    OutputTruncated: z.boolean(),
    MovedToBackground: z.boolean(),
    ShellExitCode: optionalNullable(z.number().int()),
  })
  .strict();
export const toolRowSchema = z
  .object({
    StepID: optionalIdentifier,
    ToolCallID: identifier,
    ToolName: identifier,
    Text: z.string(),
    IsError: z.boolean(),
    ResultSummary: optionalText,
    CondensedText: optionalText,
    Presentation: optionalNullable(toolMetaSchema),
  })
  .strict();
export const reasoningIdentitySchema = z
  .object({
    Provider: optionalNullable(
      z
        .object({ ItemID: identifier, SummaryIndex: optionalNullable(z.number().int().nonnegative()) })
        .strict(),
    ),
    Kent: optionalIdentifier,
  })
  .strict();
export const reasoningRowSchema = z
  .object({
    StepID: identifier,
    CompactText: identifier,
    Text: identifier,
    duration_ms: optionalNullable(z.number().int().nonnegative()),
    ProvisionalIdentity: optionalNullable(reasoningIdentitySchema),
  })
  .strict();
export const noticeSchema = z
  .object({
    StepID: optionalIdentifier,
    Reason: z.enum([
      "cache_warning",
      "compaction",
      "legacy_untyped_notice",
      "runtime_diagnostic",
      "tool_output_repair",
      "provider_model_mismatch",
    ]),
    Severity: z.enum(["info", "warning", "error"]),
    MessageType: optionalNullable(identifier),
    LegacyText: optionalText,
    NoticeID: optionalIdentifier,
    SourcePath: optionalText,
    Worktree: optionalNullable(
      z
        .object({
          Branch: optionalText,
          WorktreePath: identifier,
          WorkspaceRoot: identifier,
          EffectiveCwd: identifier,
        })
        .strict(),
    ),
    CacheWarning: optionalNullable(
      z
        .object({
          Scope: identifier,
          Reason: identifier,
          LostInputTokens: optionalNullable(z.number().int().positive()),
          Visibility: z.string(),
        })
        .strict(),
    ),
    Compaction: optionalNullable(
      z.object({ Count: optionalNullable(z.number().int().positive()), Detail: optionalText }).strict(),
    ),
    ToolOutputRepair: optionalNullable(
      z
        .object({
          kind: z.enum(["fresh_resource", "live_provider_rejection"]),
          count: z.number().int().positive(),
        })
        .strict(),
    ),
    ProviderModelMismatch: optionalNullable(
      z.object({ requested_model: identifier, served_model: identifier }).strict(),
    ),
    Diagnostic: optionalNullable(diagnosticSchema),
    Background: optionalNullable(
      z
        .object({
          ActivityID: identifier,
          ProcessID: identifier,
          ExitCode: optionalNullable(z.number().int()),
        })
        .strict(),
    ),
    CondensedText: optionalText,
    CompactLabel: optionalText,
  })
  .strict();
export const committedRowSchema = z
  .object({
    Visibility: z.enum(["ongoing", "ongoing_collapsed", "detail", "hidden"]),
    Integrity: z.union([z.literal(0), z.literal(1), z.literal(2)]),
    Kind: z.enum([
      "user",
      "assistant",
      "tool",
      "reasoning_trace",
      "notice",
      "reviewer_feedback",
      "reviewer_error",
    ]),
    Locator: locatorSchema,
    User: userRowSchema.nullable(),
    Assistant: assistantRowSchema.nullable(),
    Tool: toolRowSchema.nullable(),
    ReasoningTrace: reasoningRowSchema.nullable(),
    Notice: noticeSchema.nullable(),
    ReviewerFeedback: z
      .object({
        ID: identifier,
        StepID: identifier,
        Suggestions: z.array(identifier),
        SuggestionCount: z.number().int().positive(),
      })
      .strict()
      .nullable(),
    ReviewerError: z.object({ ID: identifier, StepID: identifier, Detail: identifier }).strict().nullable(),
  })
  .strict()
  .superRefine((row, context) => {
    const payloads = [
      row.User,
      row.Assistant,
      row.Tool,
      row.ReasoningTrace,
      row.Notice,
      row.ReviewerFeedback,
      row.ReviewerError,
    ];
    const present = payloads.filter((payload) => payload !== null).length;
    if (present !== 1) context.addIssue({ code: "custom", message: "committed row requires one payload" });
    const expected = [
      "user",
      "assistant",
      "tool",
      "reasoning_trace",
      "notice",
      "reviewer_feedback",
      "reviewer_error",
    ][payloads.findIndex((payload) => payload !== null)];
    if (row.Kind !== expected)
      context.addIssue({ code: "custom", message: "committed row kind does not match payload" });
  });
export const pageSchema = z
  .object({
    transcript: z
      .object({
        SessionID: nonBlank,
        SessionName: z.string(),
        ConversationFreshness: z.number().int().nonnegative(),
        OlderCursor: z.number().int().positive().nullable().optional(),
        HasMoreAbove: z.boolean(),
        NewerCursor: z.number().int().positive().nullable().optional(),
        HasMoreBelow: z.boolean(),
        LatestRollbackCandidate: z
          .object({
            user_message_seq: z.number().int().positive(),
            candidate_page_end_byte: z.number().int().positive(),
          })
          .strict()
          .nullable()
          .optional(),
        Entries: z.array(committedRowSchema),
      })
      .strict(),
  })
  .strict();
export const sessionIdentitySchema = z
  .object({
    SessionID: identifier,
    SessionName: optionalNullable(identifier),
    ConversationFreshness: z.union([z.literal(0), z.literal(1)]),
    ExecutionTarget: executionTargetSchema.nullable(),
  })
  .strict()
  .transform((value) => ({ ...value, SessionName: value.SessionName ?? null }));
export const sessionStatusSchema = z
  .object({
    ReviewerFrequency: identifier,
    ReviewerEnabled: z.boolean(),
    AutoCompactionEnabled: z.boolean(),
    QuestionsEnabled: z.boolean(),
    FastModeAvailable: z.boolean(),
    FastModeEnabled: z.boolean(),
    ThinkingLevel: identifier,
    CompactionMode: identifier,
    CompactionCount: z.number().int().nonnegative(),
    PreviousSessionID: optionalIdentifier,
    ParentAgentSessionID: optionalIdentifier,
    NavigationTargetSessionID: optionalIdentifier,
    Workflow: z.object({ TaskID: identifier, WorkflowID: identifier }).nullable(),
  })
  .strict();
export const runtimeReadModelUpdateSchema = z
  .object({
    Version: z
      .object({
        Epoch: identifier,
        Generation: z.number().int().positive(),
        Sequence: z.number().int().positive(),
      })
      .strict(),
    Activity: runtimeActivitySchema,
  })
  .strict();
export const hydrationSchema = z
  .object({
    SessionIdentity: sessionIdentitySchema,
    SessionStatus: sessionStatusSchema,
    RuntimeReadModelUpdate: runtimeReadModelUpdateSchema,
    CommittedRows: z.array(committedRowSchema),
    ActiveAssistant: z
      .object({
        StepID: identifier,
        StreamID: identifier,
        Text: identifier,
        Phase: z.enum(["commentary", "final_answer"]),
      })
      .strict()
      .nullable(),
    ActiveThinkingStatus: z.object({ StepID: identifier, Text: identifier }).strict().nullable(),
    ActiveReasoningTraces: z.array(
      z
        .object({
          StepID: identifier,
          Identity: reasoningIdentitySchema,
          CompactText: identifier,
          Text: identifier,
        })
        .strict(),
    ),
    ActiveStep: z
      .object({
        RunID: identifier,
        StepID: identifier,
        ActiveKind: identifier,
        Lifecycle: z.string(),
        Status: z.string(),
      })
      .strict()
      .nullable(),
    ActiveCompaction: z
      .object({
        StepID: identifier,
        RequestID: optionalIdentifier,
        State: z.string(),
        Mode: z.string(),
        Count: z.number().int(),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict()
      .nullable(),
    InFlightTools: z.array(
      z
        .object({
          StepID: identifier,
          ToolCallID: identifier,
          ToolName: identifier,
          Presentation: optionalNullable(toolMetaSchema),
        })
        .strict(),
    ),
    PendingPrompts: z.array(
      z
        .object({
          Kind: z.enum(["question", "approval"]),
          State: z.enum(["pending", "resolved"]),
          PromptID: identifier,
          SessionID: identifier,
          StepID: identifier,
          Question: identifier,
          CreatedAt: timestamp,
          Suggestions: z.array(z.string()),
          RecommendedOptionIndex: optionalNullable(z.number().int()),
          ApprovalOptions: z.array(z.string()),
          Tool: z.object({ ToolCallID: identifier, ToolName: identifier }).strict().nullable(),
        })
        .strict(),
    ),
    BackgroundActivities: z.array(
      z
        .object({
          ActivityID: identifier,
          ProcessID: identifier,
          OwnerRunID: identifier,
          OwnerStepID: identifier,
          Lifecycle: z.enum(["backgrounded", "completed", "killed"]),
          Command: identifier,
          Workdir: identifier,
          LogPath: optionalText,
          Preview: optionalText,
          ExitCode: optionalNullable(z.number().int()),
          UserRequestedKill: z.boolean(),
          NoticeSuppressed: z.boolean(),
          Diagnostic: optionalNullable(diagnosticSchema),
        })
        .strict(),
    ),
    ContextUsage: z
      .object({
        UsedTokens: z.number().int().nonnegative(),
        WindowTokens: z.number().int().positive(),
        CacheHitPercent: optionalNullable(z.number().int().nonnegative()),
      })
      .strict()
      .nullable(),
    GoalStatus: z
      .object({
        Goal: z
          .object({ ...goalSchema.shape, Suspended: z.boolean() })
          .strict()
          .nullable(),
        Availability: z.enum(["available", "agent_capability_missing"]).nullable(),
      })
      .strict()
      .nullable(),
  })
  .strict();

export type ChatCommittedRowWire = z.output<typeof committedRowSchema>;
export type ChatDiagnosticWire = z.output<typeof diagnosticSchema>;
export type ChatHydrationWire = z.output<typeof hydrationSchema>;
export type ChatReasoningIdentityWire = z.output<typeof reasoningIdentitySchema>;
export type ChatRuntimeReadModelUpdateWire = z.output<typeof runtimeReadModelUpdateSchema>;
export type ChatSessionIdentityWire = z.output<typeof sessionIdentitySchema>;
export type ChatSessionStatusWire = z.output<typeof sessionStatusSchema>;
export type ChatToolMetaWire = z.output<typeof toolMetaSchema>;
export type ChatBackgroundActivityWire = ChatHydrationWire["BackgroundActivities"][number];
export type ChatPromptWire = ChatHydrationWire["PendingPrompts"][number];

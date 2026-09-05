import { z } from "zod";

import { patchPresentationSchema } from "./chatPatchSchemas";

export const nonBlank = z.string().refine((value) => value.trim().length > 0);
export const optionalNullable = <T extends z.ZodType>(schema: T) => schema.nullable().optional();
export const nullableArray = <T extends z.ZodType>(schema: T) =>
  z
    .array(schema)
    .nullable()
    .transform((value) => value ?? []);
export const timestamp = z.iso.datetime({ offset: true });
export const identifier = nonBlank;
export const optionalIdentifier = optionalNullable(identifier);
export const text = nonBlank;
export const nonEmptyText = z.string().min(1);
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
  .strict();
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
            values: z.array(nonBlank).optional(),
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
    Text: text,
    CondensedText: optionalText,
    RollbackTargetID: optionalText,
    committed_at_unix_ms: optionalNullable(committedAtSchema),
  })
  .strict();
export const assistantRowSchema = z
  .object({
    StepID: identifier,
    StreamID: optionalIdentifier,
    Text: text,
    CondensedText: optionalText,
    Phase: z.enum(["commentary", "final_answer"]),
    committed_at_unix_ms: optionalNullable(committedAtSchema),
  })
  .strict();
const patchPresentationOwnerNames = new Set(["patch", "edit", "replace", "write"]);
export const toolMetaSchema = z
  .object({
    ToolName: z.string(),
    Presentation: z.enum(["default", "shell", "ask_question"]),
    RenderBehavior: z.string(),
    IsShell: z.boolean(),
    UserInitiated: z.boolean(),
    Command: z.string(),
    CompactText: z.string(),
    InlineMeta: z.string(),
    TimeoutLabel: z.string(),
    PatchPresentation: patchPresentationSchema.nullable(),
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
    Suggestions: nullableArray(z.string()),
    RecommendedOptionIndex: z.number().int(),
    OmitSuccessfulResult: z.boolean(),
    RawOutputRequested: z.boolean(),
    OutputTruncated: z.boolean(),
    MovedToBackground: z.boolean(),
    ShellExitCode: optionalNullable(z.number().int()),
  })
  .strict()
  .superRefine((meta, context) => {
    const ownsPatchPresentation = patchPresentationOwnerNames.has(meta.ToolName);
    if (ownsPatchPresentation !== (meta.PatchPresentation != null)) {
      context.addIssue({
        code: "custom",
        path: ["PatchPresentation"],
        message: ownsPatchPresentation
          ? "Patch/Edit presentation is required"
          : "non-Patch tool cannot carry Patch/Edit presentation",
      });
    }
  });

export function validateToolPresentationOwner(
  owner: Readonly<{
    ToolName: string;
    Presentation?: z.output<typeof toolMetaSchema> | null | undefined;
  }>,
  context: z.RefinementCtx,
) {
  if (patchPresentationOwnerNames.has(owner.ToolName) && owner.Presentation == null) {
    context.addIssue({
      code: "custom",
      path: ["Presentation"],
      message: "Patch/Edit presentation is required",
    });
    return;
  }
  if (owner.Presentation != null && owner.Presentation.ToolName !== owner.ToolName) {
    context.addIssue({
      code: "custom",
      path: ["Presentation", "ToolName"],
      message: "presentation tool name does not match tool identity",
    });
  }
}
export const questionAnswerSchema = z
  .object({
    SelectedOptionNumber: optionalNullable(z.number().int()),
    Freeform: optionalNullable(z.string()),
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
    QuestionAnswer: optionalNullable(questionAnswerSchema),
  })
  .strict()
  .superRefine(validateToolPresentationOwner);
export const reasoningIdentitySchema = z
  .object({
    Provider: z
      .object({ ItemID: identifier, SummaryIndex: z.number().int().nonnegative() })
      .strict()
      .nullable()
      .optional(),
    Kent: optionalIdentifier,
  })
  .strict();
export const reasoningRowSchema = z
  .object({
    StepID: identifier,
    CompactText: text,
    Text: text,
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
const committedRowBaseSchema = z
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
  .strict();
export const committedRowSchema = committedRowBaseSchema.superRefine((row, context) => {
  if (row.Kind !== "tool") return;
  const tool = row.Tool;
  if (tool === null) return;
  const presentation = tool.Presentation;
  if (presentation?.Presentation === undefined) return;
  if (presentation.Presentation !== "ask_question") return;

  const recommendation = presentation.RecommendedOptionIndex;
  if (recommendation < 0 || recommendation > presentation.Suggestions.length) {
    context.addIssue({
      code: "custom",
      path: ["Tool", "Presentation", "RecommendedOptionIndex"],
      message: "Ask Question recommendation is outside the offered option range.",
    });
  }
  if (tool.IsError) {
    if (tool.Text.trim().length === 0) {
      context.addIssue({
        code: "custom",
        path: ["Tool", "Text"],
        message: "Failed Ask Question rows require nonblank tool text.",
      });
    }
  } else if (tool.QuestionAnswer == null) {
    context.addIssue({
      code: "custom",
      path: ["Tool", "QuestionAnswer"],
      message: "Successful Ask Question rows require typed answer facts.",
    });
  }
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

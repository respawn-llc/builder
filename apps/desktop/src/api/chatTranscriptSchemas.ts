import { z } from "zod";

import type { ChatTranscriptKind, ChatTranscriptPayloadByKind } from "./chatTypes";
import {
  committedRowSchema,
  diagnosticSchema,
  goalSchema,
  hydrationSchema,
  identifier,
  optionalIdentifier,
  optionalNullable,
  optionalText,
  record,
  reasoningIdentitySchema,
  runtimeReadModelUpdateSchema,
  sessionIdentitySchema,
  sessionStatusSchema,
  timestamp,
  toolMetaSchema,
} from "./chatSchemas";

export function transcriptPayloadSchema<Kind extends ChatTranscriptKind>(
  kind: Kind,
): z.ZodType<ChatTranscriptPayloadByKind[Kind]> {
  const schemas: { [Key in ChatTranscriptKind]: z.ZodType<ChatTranscriptPayloadByKind[Key]> } = {
    hydration: hydrationSchema,
    committed_row: committedRowSchema,
    assistant_delta: z
      .object({
        StepID: identifier,
        StreamID: identifier,
        Delta: identifier,
        Phase: z.enum(["commentary", "final_answer"]),
      })
      .strict(),
    assistant_stream_abort: z
      .object({
        StepID: identifier,
        StreamID: identifier,
        Reason: z.enum(["interrupted", "failed", "superseded"]),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
    thinking_status_update: z.object({ StepID: identifier, Text: identifier }).strict(),
    reasoning_trace_update: z
      .object({
        StepID: identifier,
        Identity: reasoningIdentitySchema,
        CompactText: identifier,
        Text: identifier,
      })
      .strict(),
    reasoning_trace_reset: z.object({ StepID: identifier }).strict(),
    tool_start: z
      .object({
        StepID: identifier,
        ToolCallID: identifier,
        ToolName: identifier,
        Presentation: optionalNullable(toolMetaSchema),
      })
      .strict(),
    tool_abort: z
      .object({
        StepID: identifier,
        ToolCallID: identifier,
        Reason: z.enum(["canceled", "failed"]),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
    user_message_flushed: z.object({ StepID: optionalIdentifier }).strict(),
    queued_message_state: z
      .object({
        QueueItemID: identifier,
        Status: z.enum(["accepted", "submitted", "failed", "discarded"]),
        FailureReason: optionalNullable(
          z.enum(["closing", "terminal_workflow_completion", "runtime_unavailable"]),
        ),
        Text: optionalText,
      })
      .strict(),
    pending_work_changed: z.object({}).strict(),
    pending_work_restored: z
      .object({
        Restoration: z.object({ ItemID: identifier, Kind: identifier, CanonicalInput: identifier }).strict(),
      })
      .strict(),
    session_setting_feedback: z
      .object({
        Kind: z.enum(["session_name", "thinking", "fast_mode", "supervisor", "questions", "auto_compaction"]),
        Changed: z.boolean(),
        SessionName: optionalText,
        Thinking: optionalText,
        FastMode: optionalNullable(z.boolean()),
        Supervisor: optionalText,
        Questions: optionalNullable(z.boolean()),
        AutoCompaction: optionalNullable(z.boolean()),
      })
      .strict(),
    human_input_interrupted: z
      .object({
        Items: z.array(z.object({ QueueItemID: identifier, Text: identifier }).strict()),
      })
      .strict(),
    step_state: z
      .object({
        RunID: identifier,
        StepID: identifier,
        Lifecycle: z.enum(["started", "finished"]),
        ActiveKind: identifier,
        Status: z.enum(["running", "completed", "interrupted", "failed"]),
      })
      .strict(),
    runtime_read_model_update: runtimeReadModelUpdateSchema,
    session_status: sessionStatusSchema,
    session_identity: sessionIdentitySchema,
    compaction_status: z
      .object({
        StepID: identifier,
        RequestID: optionalIdentifier,
        State: z.enum(["started", "completed", "failed"]),
        Mode: z.enum(["auto", "handoff", "manual", "workflow_post_completion"]),
        Count: z.number().int().nonnegative(),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
    context_usage: z
      .object({
        UsedTokens: z.number().int().nonnegative(),
        WindowTokens: z.number().int().positive(),
        CacheHitPercent: optionalNullable(z.number().int().nonnegative()),
      })
      .strict(),
    goal_status: z
      .object({
        Goal: goalSchema.extend({ Suspended: z.boolean() }).nullable(),
        Availability: z.enum(["available", "agent_capability_missing"]).nullable(),
      })
      .strict(),
    background_activity: z
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
    prompt: z
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
    worktree_transition_outcome: z
      .object({
        OperationID: identifier,
        Transition: z.enum(["enter", "leave", "delete"]),
        State: z.enum(["completed", "failed"]),
        Failure: optionalNullable(diagnosticSchema),
        SelectorError: optionalNullable(record),
        DeletePrecondition: optionalNullable(
          z
            .object({
              Kind: identifier,
              DirtyFileCount: optionalNullable(z.number().int()),
              UnknownCause: optionalText,
            })
            .strict(),
        ),
      })
      .strict(),
    operational_diagnostic: z
      .object({
        Code: z.enum([
          "sleep_guard_failed",
          "prompt_history_persist_failed",
          "context_facts_persist_failed",
          "in_flight_clear_failed",
          "provider_turn_state_invalid",
        ]),
        StepID: optionalIdentifier,
        Detail: z.string(),
      })
      .strict(),
    live_run_finished: z
      .object({
        Status: z.enum(["completed", "interrupted", "failed"]),
        ResultKind: z.enum(["assistant_final_answer", "no_final_answer"]),
        NoFinalReason: z.string(),
        WorkPerformed: z.boolean(),
        FinalAnswer: optionalText,
        Failure: optionalText,
        StartedAt: timestamp,
        FinishedAt: timestamp,
      })
      .strict(),
  };
  return schemas[kind];
}
export const transcriptEventSchema = z
  .object({
    message: z
      .object({
        Sequence: z.number().int().positive(),
        Kind: z.enum([
          "hydration",
          "committed_row",
          "assistant_delta",
          "assistant_stream_abort",
          "thinking_status_update",
          "reasoning_trace_update",
          "reasoning_trace_reset",
          "tool_start",
          "tool_abort",
          "user_message_flushed",
          "queued_message_state",
          "pending_work_changed",
          "pending_work_restored",
          "session_setting_feedback",
          "human_input_interrupted",
          "step_state",
          "runtime_read_model_update",
          "session_status",
          "session_identity",
          "compaction_status",
          "context_usage",
          "goal_status",
          "background_activity",
          "prompt",
          "worktree_transition_outcome",
          "operational_diagnostic",
          "live_run_finished",
        ]),
        Payload: record,
      })
      .strict(),
  })
  .strict();

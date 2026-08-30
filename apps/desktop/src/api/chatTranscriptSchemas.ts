import { z } from "zod";

import type { ChatTranscriptKind, ChatTranscriptPayloadByKind } from "./chatTypes";
import {
  committedRowSchema,
  diagnosticSchema,
  goalSchema,
  identifier,
  nonEmptyText,
  nonBlank,
  optionalIdentifier,
  optionalNullable,
  optionalText,
  reasoningIdentitySchema,
  runtimeReadModelUpdateSchema,
  sessionIdentitySchema,
  sessionStatusSchema,
  text,
  timestamp,
  toolMetaSchema,
} from "./chatSchemas";
import { hydrationSchema, promptSchema } from "./chatHydrationSchemas";

const selectorErrorKindSchema = z.union([z.literal(1), z.literal(2), z.literal(3)]);
const topologyVariantSchema = z.union([z.literal(1), z.literal(2), z.literal(3)]);
const selectorCandidateSchema = z
  .object({
    variant: topologyVariantSchema,
    selector: identifier,
    branch_name: nonBlank.optional(),
    display_name: nonBlank.optional(),
    fallback_identity: identifier,
  })
  .strict();
const selectorErrorDetailsSchema = z
  .object({
    kind: selectorErrorKindSchema,
    input: text,
    candidates: z.array(selectorCandidateSchema).optional(),
  })
  .strict()
  .superRefine((value, context) => {
    const hasCandidates = value.candidates !== undefined && value.candidates.length > 0;
    if (value.kind === 2 && !hasCandidates) {
      context.addIssue({
        code: "custom",
        path: ["candidates"],
        message: "ambiguous selector errors require candidates",
      });
    }
    if ((value.kind === 1 || value.kind === 3) && hasCandidates) {
      context.addIssue({
        code: "custom",
        path: ["candidates"],
        message: "only ambiguous selector errors may contain candidates",
      });
    }
  });
const deletePreconditionSchema = z
  .object({
    kind: z.enum(["dirty", "unknown"]),
    dirty_file_count: z.number().int().positive().optional(),
    unknown_cause: nonBlank.optional(),
  })
  .strict()
  .superRefine((value, context) => {
    if (
      value.kind === "dirty" &&
      (value.dirty_file_count === undefined || value.unknown_cause !== undefined)
    ) {
      context.addIssue({ code: "custom", message: "dirty preconditions require a positive count only" });
    }
    if (
      value.kind === "unknown" &&
      (value.dirty_file_count !== undefined || value.unknown_cause === undefined)
    ) {
      context.addIssue({ code: "custom", message: "unknown preconditions require an unknown cause only" });
    }
  });

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
        Delta: nonEmptyText,
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
    thinking_status_update: z.object({ StepID: identifier, Text: text }).strict(),
    reasoning_trace_update: z
      .object({
        StepID: identifier,
        Identity: reasoningIdentitySchema,
        CompactText: text,
        Text: text,
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
        Restoration: z.object({ ItemID: identifier, Kind: identifier, CanonicalInput: text }).strict(),
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
        Items: z.array(z.object({ QueueItemID: identifier, Text: text }).strict()),
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
        Command: text,
        Workdir: identifier,
        LogPath: optionalText,
        Preview: optionalText,
        ExitCode: optionalNullable(z.number().int()),
        UserRequestedKill: z.boolean(),
        NoticeSuppressed: z.boolean(),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
    prompt: promptSchema,
    worktree_transition_outcome: z
      .object({
        OperationID: identifier,
        Transition: z.enum(["enter", "leave", "delete"]),
        State: z.enum(["completed", "failed"]),
        Failure: optionalNullable(diagnosticSchema),
        SelectorError: optionalNullable(selectorErrorDetailsSchema),
        DeletePrecondition: optionalNullable(deletePreconditionSchema),
      })
      .strict()
      .superRefine((value, context) => {
        validateWorktreeTransition(value, context);
      }),
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
      .strict()
      .superRefine((value, context) => {
        if (value.Code === "provider_turn_state_invalid") {
          if (value.Detail !== "") {
            context.addIssue({
              code: "custom",
              path: ["Detail"],
              message: "provider turn-state diagnostics have no detail",
            });
          }
        } else if (value.Detail.trim().length === 0) {
          context.addIssue({ code: "custom", path: ["Detail"], message: "diagnostic detail is required" });
        }
      }),
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

type WorktreeTransitionValue = Readonly<{
  Transition: "enter" | "leave" | "delete";
  State: "completed" | "failed";
  Failure?: unknown;
  SelectorError?: unknown;
  DeletePrecondition?: unknown;
}>;

function validateWorktreeTransition(value: WorktreeTransitionValue, context: z.RefinementCtx): void {
  if (value.State === "completed") {
    validateCompletedWorktreeTransition(value, context);
    return;
  }
  validateFailedWorktreeTransition(value, context);
}

function validateCompletedWorktreeTransition(value: WorktreeTransitionValue, context: z.RefinementCtx): void {
  if (
    (value.Failure !== undefined && value.Failure !== null) ||
    (value.SelectorError !== undefined && value.SelectorError !== null) ||
    (value.DeletePrecondition !== undefined && value.DeletePrecondition !== null)
  ) {
    context.addIssue({
      code: "custom",
      message: "completed transitions cannot carry failure details",
    });
  }
}

function validateFailedWorktreeTransition(value: WorktreeTransitionValue, context: z.RefinementCtx): void {
  const hasFailure = value.Failure !== undefined && value.Failure !== null;
  const hasSelectorError = value.SelectorError !== undefined && value.SelectorError !== null;
  const hasDeletePrecondition = value.DeletePrecondition !== undefined && value.DeletePrecondition !== null;
  if (hasFailure === hasSelectorError) {
    context.addIssue({
      code: "custom",
      message: "failed transitions require exactly one failure detail",
    });
  }
  if (hasDeletePrecondition && (value.Transition !== "delete" || !hasFailure || hasSelectorError)) {
    context.addIssue({
      code: "custom",
      path: ["DeletePrecondition"],
      message: "delete preconditions require a failed delete with a diagnostic",
    });
  }
}

function transcriptMessageVariant<Kind extends ChatTranscriptKind>(kind: Kind) {
  return z
    .object({
      Sequence: kind === "hydration" ? z.literal(1) : z.number().int().min(2),
      Kind: z.literal(kind),
      Payload: transcriptPayloadSchema(kind),
    })
    .strict()
    .transform((value) => ({
      sequence: value.Sequence,
      kind: value.Kind,
      payload: value.Payload,
    }));
}

export const transcriptMessageSchema = z.discriminatedUnion("Kind", [
  transcriptMessageVariant("hydration"),
  transcriptMessageVariant("committed_row"),
  transcriptMessageVariant("assistant_delta"),
  transcriptMessageVariant("assistant_stream_abort"),
  transcriptMessageVariant("thinking_status_update"),
  transcriptMessageVariant("reasoning_trace_update"),
  transcriptMessageVariant("reasoning_trace_reset"),
  transcriptMessageVariant("tool_start"),
  transcriptMessageVariant("tool_abort"),
  transcriptMessageVariant("user_message_flushed"),
  transcriptMessageVariant("queued_message_state"),
  transcriptMessageVariant("pending_work_changed"),
  transcriptMessageVariant("pending_work_restored"),
  transcriptMessageVariant("session_setting_feedback"),
  transcriptMessageVariant("human_input_interrupted"),
  transcriptMessageVariant("step_state"),
  transcriptMessageVariant("runtime_read_model_update"),
  transcriptMessageVariant("session_status"),
  transcriptMessageVariant("session_identity"),
  transcriptMessageVariant("compaction_status"),
  transcriptMessageVariant("context_usage"),
  transcriptMessageVariant("goal_status"),
  transcriptMessageVariant("background_activity"),
  transcriptMessageVariant("prompt"),
  transcriptMessageVariant("worktree_transition_outcome"),
  transcriptMessageVariant("operational_diagnostic"),
  transcriptMessageVariant("live_run_finished"),
]);

export const transcriptEventSchema = z
  .object({
    message: transcriptMessageSchema,
  })
  .strict();

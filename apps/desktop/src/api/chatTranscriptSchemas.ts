import { z } from "zod";

import {
  committedRowSchema,
  diagnosticSchema,
  identifier,
  nonBlank,
  optionalIdentifier,
  optionalNullable,
  optionalText,
  runtimeReadModelUpdateSchema,
  sessionIdentitySchema,
  sessionStatusSchema,
  text,
  timestamp,
} from "./chatSchemas";
import { hydrationSchema, promptSchema } from "./chatHydrationSchemas";
import {
  assistantStreamContentSchema,
  assistantStreamFactsSchema,
  backgroundActivitySchema,
  compactionStatusSchema,
  goalStatusSchema,
  inFlightToolSchema,
  reasoningTraceSchema,
  stepStateSchema,
  transcriptContextUsageSchema,
  thinkingStatusSchema,
} from "./chatTranscriptFactSchemas";

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
  .strict();
const deletePreconditionSchema = z
  .object({
    kind: z.enum(["dirty", "unknown"]),
    dirty_file_count: z.number().int().positive().optional(),
    unknown_cause: nonBlank.optional(),
  })
  .strict();

const transcriptPayloadSchemas = {
  hydration: hydrationSchema,
  committed_row: committedRowSchema,
  assistant_delta: assistantStreamFactsSchema.extend({ Delta: assistantStreamContentSchema }),
  assistant_stream_abort: z
    .object({
      StepID: identifier,
      StreamID: identifier,
      Reason: z.enum(["interrupted", "failed", "superseded"]),
      Diagnostic: optionalNullable(diagnosticSchema),
    })
    .strict(),
  thinking_status_update: thinkingStatusSchema,
  reasoning_trace_update: reasoningTraceSchema,
  reasoning_trace_reset: z.object({ StepID: identifier }).strict(),
  tool_start: inFlightToolSchema,
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
  step_state: stepStateSchema,
  runtime_read_model_update: runtimeReadModelUpdateSchema,
  session_status: sessionStatusSchema,
  session_identity: sessionIdentitySchema,
  compaction_status: compactionStatusSchema,
  context_usage: transcriptContextUsageSchema,
  goal_status: goalStatusSchema,
  background_activity: backgroundActivitySchema,
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
} as const;

type DeepReadonly<Value> = Value extends (...args: never[]) => unknown
  ? Value
  : Value extends readonly (infer Element)[]
    ? readonly DeepReadonly<Element>[]
    : Value extends object
      ? { readonly [Key in keyof Value]: DeepReadonly<Value[Key]> }
      : Value;

export type ChatTranscriptKind = keyof typeof transcriptPayloadSchemas;
export type ChatTranscriptPayloadByKind = {
  [Kind in ChatTranscriptKind]: DeepReadonly<z.output<(typeof transcriptPayloadSchemas)[Kind]>>;
};
export type ChatTranscriptPayload = ChatTranscriptPayloadByKind[ChatTranscriptKind];
export type ChatTranscriptMessageByKind = {
  [Kind in ChatTranscriptKind]: Readonly<{
    sequence: number;
    kind: Kind;
    payload: ChatTranscriptPayloadByKind[Kind];
  }>;
}[ChatTranscriptKind];
export type ChatTranscriptMessage = ChatTranscriptMessageByKind;

export function transcriptPayloadSchema<Kind extends ChatTranscriptKind>(
  kind: Kind,
): (typeof transcriptPayloadSchemas)[Kind] {
  return transcriptPayloadSchemas[kind];
}

function transcriptMessageVariant<Kind extends ChatTranscriptKind, Output, Input>(
  kind: Kind,
  payloadSchema: z.ZodType<Output, Input>,
) {
  return z
    .object({
      Sequence: z.number().int(),
      Kind: z.literal(kind),
      Payload: payloadSchema,
    })
    .strict()
    .transform((value) => ({
      sequence: value.Sequence,
      kind: value.Kind,
      payload: value.Payload,
    }));
}

export const transcriptMessageSchema = z.discriminatedUnion("Kind", [
  transcriptMessageVariant("hydration", transcriptPayloadSchemas.hydration),
  transcriptMessageVariant("committed_row", transcriptPayloadSchemas.committed_row),
  transcriptMessageVariant("assistant_delta", transcriptPayloadSchemas.assistant_delta),
  transcriptMessageVariant("assistant_stream_abort", transcriptPayloadSchemas.assistant_stream_abort),
  transcriptMessageVariant("thinking_status_update", transcriptPayloadSchemas.thinking_status_update),
  transcriptMessageVariant("reasoning_trace_update", transcriptPayloadSchemas.reasoning_trace_update),
  transcriptMessageVariant("reasoning_trace_reset", transcriptPayloadSchemas.reasoning_trace_reset),
  transcriptMessageVariant("tool_start", transcriptPayloadSchemas.tool_start),
  transcriptMessageVariant("tool_abort", transcriptPayloadSchemas.tool_abort),
  transcriptMessageVariant("user_message_flushed", transcriptPayloadSchemas.user_message_flushed),
  transcriptMessageVariant("queued_message_state", transcriptPayloadSchemas.queued_message_state),
  transcriptMessageVariant("pending_work_changed", transcriptPayloadSchemas.pending_work_changed),
  transcriptMessageVariant("pending_work_restored", transcriptPayloadSchemas.pending_work_restored),
  transcriptMessageVariant("session_setting_feedback", transcriptPayloadSchemas.session_setting_feedback),
  transcriptMessageVariant("human_input_interrupted", transcriptPayloadSchemas.human_input_interrupted),
  transcriptMessageVariant("step_state", transcriptPayloadSchemas.step_state),
  transcriptMessageVariant("runtime_read_model_update", transcriptPayloadSchemas.runtime_read_model_update),
  transcriptMessageVariant("session_status", transcriptPayloadSchemas.session_status),
  transcriptMessageVariant("session_identity", transcriptPayloadSchemas.session_identity),
  transcriptMessageVariant("compaction_status", transcriptPayloadSchemas.compaction_status),
  transcriptMessageVariant("context_usage", transcriptPayloadSchemas.context_usage),
  transcriptMessageVariant("goal_status", transcriptPayloadSchemas.goal_status),
  transcriptMessageVariant("background_activity", transcriptPayloadSchemas.background_activity),
  transcriptMessageVariant("prompt", transcriptPayloadSchemas.prompt),
  transcriptMessageVariant(
    "worktree_transition_outcome",
    transcriptPayloadSchemas.worktree_transition_outcome,
  ),
  transcriptMessageVariant("operational_diagnostic", transcriptPayloadSchemas.operational_diagnostic),
  transcriptMessageVariant("live_run_finished", transcriptPayloadSchemas.live_run_finished),
]);

export const transcriptEventSchema = z
  .object({
    message: transcriptMessageSchema,
  })
  .strict();

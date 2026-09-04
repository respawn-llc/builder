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

type DeepReadonly<Value> = Value extends (...args: never[]) => unknown
  ? Value
  : Value extends readonly (infer Element)[]
    ? readonly DeepReadonly<Element>[]
    : Value extends object
      ? { readonly [Key in keyof Value]: DeepReadonly<Value[Key]> }
      : Value;

function transcriptMessageVariant<Kind extends string, Output, Input>(
  kind: Kind,
  payloadSchema: z.ZodType<Output, Input>,
) {
  return z
    .object({
      sequence: z.number().int(),
      kind: z.literal(kind),
      payload: payloadSchema,
    })
    .strict()
    .transform((value) => ({
      sequence: value.sequence,
      kind: value.kind,
      payload: value.payload,
    }));
}

const transcriptMessageVariants = [
  transcriptMessageVariant("hydration", hydrationSchema),
  transcriptMessageVariant("committed_row", committedRowSchema),
  transcriptMessageVariant(
    "assistant_delta",
    assistantStreamFactsSchema.extend({ Delta: assistantStreamContentSchema }),
  ),
  transcriptMessageVariant(
    "assistant_stream_abort",
    z
      .object({
        StepID: identifier,
        StreamID: identifier,
        Reason: z.enum(["interrupted", "failed", "superseded"]),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
  ),
  transcriptMessageVariant("thinking_status_update", thinkingStatusSchema),
  transcriptMessageVariant("reasoning_trace_update", reasoningTraceSchema),
  transcriptMessageVariant("reasoning_trace_reset", z.object({ StepID: identifier }).strict()),
  transcriptMessageVariant("tool_start", inFlightToolSchema),
  transcriptMessageVariant(
    "tool_abort",
    z
      .object({
        StepID: identifier,
        ToolCallID: identifier,
        Reason: z.enum(["canceled", "failed"]),
        Diagnostic: optionalNullable(diagnosticSchema),
      })
      .strict(),
  ),
  transcriptMessageVariant("user_message_flushed", z.object({ StepID: optionalIdentifier }).strict()),
  transcriptMessageVariant(
    "queued_message_state",
    z
      .object({
        QueueItemID: identifier,
        Status: z.enum(["accepted", "submitted", "failed", "discarded"]),
        FailureReason: optionalNullable(
          z.enum(["closing", "terminal_workflow_completion", "runtime_unavailable"]),
        ),
        Text: optionalText,
      })
      .strict(),
  ),
  transcriptMessageVariant("pending_work_changed", z.object({}).strict()),
  transcriptMessageVariant(
    "pending_work_restored",
    z
      .object({
        Restoration: z.object({ ItemID: identifier, Kind: identifier, CanonicalInput: text }).strict(),
      })
      .strict(),
  ),
  transcriptMessageVariant(
    "session_setting_feedback",
    z
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
  ),
  transcriptMessageVariant(
    "human_input_interrupted",
    z
      .object({
        Items: z.array(z.object({ QueueItemID: identifier, Text: text }).strict()),
      })
      .strict(),
  ),
  transcriptMessageVariant("step_state", stepStateSchema),
  transcriptMessageVariant("runtime_read_model_update", runtimeReadModelUpdateSchema),
  transcriptMessageVariant("session_status", sessionStatusSchema),
  transcriptMessageVariant("session_identity", sessionIdentitySchema),
  transcriptMessageVariant("compaction_status", compactionStatusSchema),
  transcriptMessageVariant("context_usage", transcriptContextUsageSchema),
  transcriptMessageVariant("goal_status", goalStatusSchema),
  transcriptMessageVariant("background_activity", backgroundActivitySchema),
  transcriptMessageVariant("prompt", promptSchema),
  transcriptMessageVariant(
    "worktree_transition_outcome",
    z
      .object({
        OperationID: identifier,
        Transition: z.enum(["enter", "leave", "delete"]),
        State: z.enum(["completed", "failed"]),
        Failure: optionalNullable(diagnosticSchema),
        SelectorError: optionalNullable(selectorErrorDetailsSchema),
        DeletePrecondition: optionalNullable(deletePreconditionSchema),
      })
      .strict(),
  ),
  transcriptMessageVariant(
    "operational_diagnostic",
    z
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
  ),
  transcriptMessageVariant(
    "live_run_finished",
    z
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
  ),
] as const;

type TranscriptMessageSchema = (typeof transcriptMessageVariants)[number];
type TranscriptMessageOutput = z.output<TranscriptMessageSchema>;

export type ChatTranscriptKind = TranscriptMessageOutput["kind"];
export type ChatTranscriptPayloadByKind = {
  [Kind in ChatTranscriptKind]: DeepReadonly<Extract<TranscriptMessageOutput, { kind: Kind }>["payload"]>;
};
export type ChatTranscriptPayload = ChatTranscriptPayloadByKind[ChatTranscriptKind];
export type ChatTranscriptMessageByKind = {
  [Kind in ChatTranscriptKind]: Readonly<{
    sequence: Extract<TranscriptMessageOutput, { kind: Kind }>["sequence"];
    kind: Kind;
    payload: ChatTranscriptPayloadByKind[Kind];
  }>;
}[ChatTranscriptKind];
export type ChatTranscriptMessage = ChatTranscriptMessageByKind;

export const transcriptMessageSchema = z.discriminatedUnion("kind", transcriptMessageVariants);

export const transcriptEventSchema = z
  .object({
    message: transcriptMessageSchema,
  })
  .strict();

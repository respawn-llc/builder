import { z } from "zod";

import {
  committedRowSchema,
  identifier,
  nullableArray,
  optionalNullable,
  runtimeReadModelUpdateSchema,
  sessionIdentitySchema,
  sessionStatusSchema,
  text,
  timestamp,
} from "./chatSchemas";
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

export const promptSchema = z
  .object({
    Kind: z.enum(["question", "approval"]),
    State: z.enum(["pending", "resolved"]),
    PromptID: identifier,
    SessionID: identifier,
    StepID: identifier,
    Question: text,
    CreatedAt: timestamp,
    Suggestions: nullableArray(text),
    RecommendedOptionIndex: optionalNullable(z.number().int()),
    ApprovalOptions: nullableArray(z.enum(["allow_once", "allow_session", "deny"])),
    Tool: z.object({ ToolCallID: identifier, ToolName: identifier }).strict().nullable(),
  })
  .strict();

export const hydrationSchema = z
  .object({
    SessionIdentity: sessionIdentitySchema,
    SessionStatus: sessionStatusSchema,
    RuntimeReadModelUpdate: runtimeReadModelUpdateSchema,
    CommittedRows: nullableArray(committedRowSchema),
    ActiveAssistant: assistantStreamFactsSchema.extend({ Text: assistantStreamContentSchema }).nullable(),
    ActiveThinkingStatus: thinkingStatusSchema.nullable(),
    ActiveReasoningTraces: nullableArray(reasoningTraceSchema),
    ActiveStep: stepStateSchema.nullable(),
    ActiveCompaction: compactionStatusSchema.nullable(),
    InFlightTools: nullableArray(inFlightToolSchema),
    PendingPrompts: nullableArray(promptSchema),
    BackgroundActivities: nullableArray(backgroundActivitySchema),
    ContextUsage: transcriptContextUsageSchema.nullable(),
    GoalStatus: goalStatusSchema.nullable(),
  })
  .strict();

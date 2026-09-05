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
    ToolCallID: identifier,
    SessionID: identifier,
    StepID: identifier,
    Question: z.string(),
    CreatedAt: timestamp,
    Suggestions: nullableArray(text),
    RecommendedOptionIndex: optionalNullable(z.number().int()),
    ApprovalOptions: nullableArray(z.enum(["allow_once", "allow_session", "deny"])),
    AccessTargets: nullableArray(z.object({ RequestedPath: text, ResolvedPath: text }).strict()),
  })
  .strict();

const tailSegmentSchema = z
  .object({
    OlderCursor: z.number().int().positive().nullable(),
    HasMoreAbove: z.boolean(),
    Entries: z.array(committedRowSchema),
  })
  .strict()
  .superRefine((segment, context) => {
    if (segment.HasMoreAbove === (segment.OlderCursor === null)) {
      context.addIssue({
        code: "custom",
        message: "OlderCursor must be present exactly when older history exists.",
        path: ["OlderCursor"],
      });
    }
  });

export const hydrationSchema = z
  .object({
    SessionIdentity: sessionIdentitySchema,
    SessionStatus: sessionStatusSchema,
    RuntimeReadModelUpdate: runtimeReadModelUpdateSchema,
    TailSegment: tailSegmentSchema,
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

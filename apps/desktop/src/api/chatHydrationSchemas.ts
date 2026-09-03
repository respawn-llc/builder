import { z } from "zod";

import {
  committedRowSchema,
  identifier,
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
    Suggestions: z.array(text),
    RecommendedOptionIndex: optionalNullable(z.number().int()),
    ApprovalOptions: z.enum(["allow_once", "allow_session", "deny"]).array(),
    Tool: z.object({ ToolCallID: identifier, ToolName: identifier }).strict().nullable(),
  })
  .strict();

export const hydrationSchema = z
  .object({
    SessionIdentity: sessionIdentitySchema,
    SessionStatus: sessionStatusSchema,
    RuntimeReadModelUpdate: runtimeReadModelUpdateSchema,
    CommittedRows: z.array(committedRowSchema),
    ActiveAssistant: assistantStreamFactsSchema.extend({ Text: assistantStreamContentSchema }).nullable(),
    ActiveThinkingStatus: thinkingStatusSchema.nullable(),
    ActiveReasoningTraces: z.array(reasoningTraceSchema),
    ActiveStep: stepStateSchema.nullable(),
    ActiveCompaction: compactionStatusSchema.nullable(),
    InFlightTools: z.array(inFlightToolSchema),
    PendingPrompts: z.array(promptSchema),
    BackgroundActivities: z.array(backgroundActivitySchema),
    ContextUsage: transcriptContextUsageSchema.nullable(),
    GoalStatus: goalStatusSchema.nullable(),
  })
  .strict();

export type ChatHydrationWire = z.output<typeof hydrationSchema>;
export type ChatBackgroundActivityWire = ChatHydrationWire["BackgroundActivities"][number];
export type ChatPromptWire = ChatHydrationWire["PendingPrompts"][number];

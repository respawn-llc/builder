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
  .strict()
  .superRefine((value, context) => {
    if (value.Kind === "question") {
      if (value.ApprovalOptions.length > 0) {
        context.addIssue({
          code: "custom",
          path: ["ApprovalOptions"],
          message: "questions cannot carry approval options",
        });
      }
      if (
        value.RecommendedOptionIndex !== undefined &&
        value.RecommendedOptionIndex !== null &&
        (value.RecommendedOptionIndex < 1 || value.RecommendedOptionIndex > value.Suggestions.length)
      ) {
        context.addIssue({
          code: "custom",
          path: ["RecommendedOptionIndex"],
          message: "recommended option index is outside the suggestions",
        });
      }
      return;
    }
    if (value.Suggestions.length > 0) {
      context.addIssue({
        code: "custom",
        path: ["Suggestions"],
        message: "approvals cannot carry suggestions",
      });
    }
    if (value.RecommendedOptionIndex !== undefined && value.RecommendedOptionIndex !== null) {
      context.addIssue({
        code: "custom",
        path: ["RecommendedOptionIndex"],
        message: "approvals cannot carry a recommended option",
      });
    }
    if (value.ApprovalOptions.length === 0) {
      context.addIssue({ code: "custom", path: ["ApprovalOptions"], message: "approvals require options" });
    }
    if (new Set(value.ApprovalOptions).size !== value.ApprovalOptions.length) {
      context.addIssue({
        code: "custom",
        path: ["ApprovalOptions"],
        message: "approval options must be unique",
      });
    }
  });

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

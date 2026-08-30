import { z } from "zod";

import {
  committedRowSchema,
  diagnosticSchema,
  goalSchema,
  identifier,
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
    ActiveAssistant: z
      .object({
        StepID: identifier,
        StreamID: identifier,
        Text: text,
        Phase: z.enum(["commentary", "final_answer"]),
      })
      .strict()
      .nullable(),
    ActiveThinkingStatus: z.object({ StepID: identifier, Text: text }).strict().nullable(),
    ActiveReasoningTraces: z.array(
      z
        .object({
          StepID: identifier,
          Identity: reasoningIdentitySchema,
          CompactText: text,
          Text: text,
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
    PendingPrompts: z.array(promptSchema),
    BackgroundActivities: z.array(
      z
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

export type ChatHydrationWire = z.output<typeof hydrationSchema>;
export type ChatBackgroundActivityWire = ChatHydrationWire["BackgroundActivities"][number];
export type ChatPromptWire = ChatHydrationWire["PendingPrompts"][number];

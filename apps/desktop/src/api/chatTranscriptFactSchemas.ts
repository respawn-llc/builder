import { z } from "zod";

import {
  diagnosticSchema,
  identifier,
  optionalIdentifier,
  optionalNullable,
  optionalText,
  reasoningIdentitySchema,
  text,
  toolMetaSchema,
} from "./chatSchemas";

export const assistantPhaseSchema = z.enum(["commentary", "final_answer"]);

export const thinkingStatusSchema = z
  .object({
    StepID: identifier,
    Text: text,
  })
  .strict();

export const reasoningTraceSchema = z
  .object({
    StepID: identifier,
    Identity: reasoningIdentitySchema,
    CompactText: text,
    Text: text,
  })
  .strict();

export const stepStateSchema = z
  .object({
    RunID: identifier,
    StepID: identifier,
    Lifecycle: z.enum(["started", "finished"]),
    ActiveKind: identifier,
    Status: z.enum(["running", "completed", "interrupted", "failed"]),
  })
  .strict();

export const compactionStatusSchema = z
  .object({
    StepID: identifier,
    RequestID: optionalIdentifier,
    State: z.enum(["started", "completed", "failed"]),
    Mode: z.enum(["auto", "handoff", "manual", "workflow_post_completion"]),
    Count: z.number().int().nonnegative(),
    Diagnostic: optionalNullable(diagnosticSchema),
  })
  .strict();

export const inFlightToolSchema = z
  .object({
    StepID: identifier,
    ToolCallID: identifier,
    ToolName: identifier,
    Presentation: optionalNullable(toolMetaSchema),
  })
  .strict();

export const backgroundActivitySchema = z
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
  .strict();

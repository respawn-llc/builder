import { z } from "zod";

import type {
  PromptAnswerBatchEntryInput,
  PromptAnswerBatchInput,
  PromptAnswerBatchResponse,
} from "./clientInputs";
import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type { JsonValue } from "./json";
import type { RpcTransport } from "./transport";

const normalizedToolCallID = z
  .string()
  .min(1)
  .refine((value) => value.trim() === value, "Tool Call ID must not have surrounding whitespace");

const responseSchema = z
  .object({
    results: z.array(
      z
        .object({
          tool_call_id: normalizedToolCallID,
          outcome: z.enum(["resolved", "skipped"]),
        })
        .strict(),
    ),
  })
  .strict()
  .transform((value): PromptAnswerBatchResponse => ({
    results: value.results.map(({ tool_call_id: toolCallID, outcome }) => ({ toolCallID, outcome })),
  }));

export async function answerPromptBatch(
  transport: RpcTransport,
  input: PromptAnswerBatchInput,
): Promise<PromptAnswerBatchResponse> {
  validateInput(input);
  const response = parseRpcResponse(
    "prompt.answerBatch",
    responseSchema,
    await transport.callAttachedSession(input.sessionID, "prompt.answerBatch", {
      session_id: input.sessionID,
      step_id: input.stepID,
      entries: input.entries.map(encodeEntry),
    }),
  );
  validateResponse(input, response);
  return response;
}

function encodeEntry(entry: PromptAnswerBatchEntryInput): JsonValue {
  switch (entry.kind) {
    case "question":
      return {
        tool_call_id: entry.toolCallID,
        question_answer: compactJsonObject({
          selected_option_number: entry.selectedOptionNumber ?? undefined,
          freeform: entry.freeform ?? undefined,
        }),
      };
    case "approval":
      return {
        tool_call_id: entry.toolCallID,
        approval_answer: compactJsonObject({
          decision: entry.decision,
          commentary: entry.commentary ?? undefined,
        }),
      };
    case "declined":
      return { tool_call_id: entry.toolCallID, declined: {} };
  }
}

function validateInput(input: PromptAnswerBatchInput): void {
  if (input.sessionID.trim().length === 0 || input.stepID.trim().length === 0 || input.entries.length === 0) {
    throw new Error("prompt answer batch requires Session, Step, and entries");
  }
}

function validateResponse(input: PromptAnswerBatchInput, response: PromptAnswerBatchResponse): void {
  if (response.results.length !== input.entries.length) {
    throw new Error("prompt answer batch result count does not match request");
  }
  const results = new Set<string>();
  for (const result of response.results) {
    if (
      !input.entries.some((entry) => entry.toolCallID === result.toolCallID) ||
      results.has(result.toolCallID)
    ) {
      throw new Error("prompt answer batch result identity is foreign or duplicated");
    }
    results.add(result.toolCallID);
  }
}

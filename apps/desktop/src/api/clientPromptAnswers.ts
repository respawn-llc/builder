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

const responseSchema = z
  .object({
    results: z.array(
      z
        .object({
          prompt_id: z.string().trim().min(1),
          outcome: z.enum(["resolved", "skipped"]),
        })
        .strict(),
    ),
  })
  .strict()
  .transform((value): PromptAnswerBatchResponse => ({
    results: value.results.map((result) => ({
      promptID: result.prompt_id,
      outcome: result.outcome,
    })),
  }));

export async function answerPromptBatch(
  transport: RpcTransport,
  input: PromptAnswerBatchInput,
): Promise<PromptAnswerBatchResponse> {
  validateInput(input);
  const response = parseRpcResponse(
    "prompt.answerBatch",
    responseSchema,
    await transport.call("prompt.answerBatch", {
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
        prompt_id: entry.promptID,
        question_answer: compactJsonObject({
          selected_option_number: entry.selectedOptionNumber ?? undefined,
          freeform: entry.freeform ?? undefined,
        }),
      };
    case "approval":
      return {
        prompt_id: entry.promptID,
        approval_answer: compactJsonObject({
          decision: entry.decision,
          commentary: entry.commentary ?? undefined,
        }),
      };
    case "declined":
      return { prompt_id: entry.promptID, declined: {} };
  }
}

function validateInput(input: PromptAnswerBatchInput): void {
  if (input.sessionID.trim().length === 0 || input.stepID.trim().length === 0 || input.entries.length === 0) {
    throw new Error("prompt answer batch requires Session, Step, and entries");
  }
  const promptIDs = new Set<string>();
  for (const entry of input.entries) {
    if (entry.promptID.trim().length === 0 || promptIDs.has(entry.promptID)) {
      throw new Error("prompt answer batch prompt identity is invalid or duplicated");
    }
    promptIDs.add(entry.promptID);
  }
}

function validateResponse(input: PromptAnswerBatchInput, response: PromptAnswerBatchResponse): void {
  if (response.results.length !== input.entries.length) {
    throw new Error("prompt answer batch result count does not match request");
  }
  const requested = new Set(input.entries.map((entry) => entry.promptID));
  const results = new Set<string>();
  for (const result of response.results) {
    if (!requested.has(result.promptID) || results.has(result.promptID)) {
      throw new Error("prompt answer batch result identity is foreign or duplicated");
    }
    results.add(result.promptID);
  }
}

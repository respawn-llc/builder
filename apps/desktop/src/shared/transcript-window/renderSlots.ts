import { ContractError, type ChatTranscriptPayloadByKind } from "@/api";

import type { CommittedRow } from "./types";

interface CommittedValues {
  user: NonNullable<CommittedRow["User"]>;
  assistant: NonNullable<CommittedRow["Assistant"]>;
  tool: NonNullable<CommittedRow["Tool"]>;
  reasoning_trace: NonNullable<CommittedRow["ReasoningTrace"]>;
  notice: NonNullable<CommittedRow["Notice"]>;
  reviewer_feedback: NonNullable<CommittedRow["ReviewerFeedback"]>;
  reviewer_error: NonNullable<CommittedRow["ReviewerError"]>;
}

export type TranscriptCommittedItem = {
  [Kind in CommittedRow["Kind"]]: Readonly<{
    kind: Kind;
    state: "committed";
    key: string;
    row: CommittedRow;
    value: CommittedValues[Kind];
  }>;
}[CommittedRow["Kind"]];

interface LiveValues {
  assistant: NonNullable<ChatTranscriptPayloadByKind["hydration"]["ActiveAssistant"]>;
  tool: ChatTranscriptPayloadByKind["tool_start"];
  reasoning_trace: ChatTranscriptPayloadByKind["reasoning_trace_update"];
}

type LiveItem = {
  [Kind in keyof LiveValues]: Readonly<{
    kind: Kind;
    state: "live";
    key: string;
    value: LiveValues[Kind];
  }>;
}[keyof LiveValues];

export type TranscriptProvisionalItem =
  | LiveItem
  | Readonly<{
      kind: "thinking_status";
      key: string;
      value: ChatTranscriptPayloadByKind["thinking_status_update"];
    }>;
export type TranscriptRenderItem = TranscriptCommittedItem | TranscriptProvisionalItem;

/** The host supplies every family, including live-or-committed variants, without a fallback renderer. */
export type TranscriptRenderSlots<Output> = Readonly<{
  user(item: Extract<TranscriptRenderItem, { kind: "user" }>): Output;
  assistant(item: Extract<TranscriptRenderItem, { kind: "assistant" }>): Output;
  tool(item: Extract<TranscriptRenderItem, { kind: "tool" }>): Output;
  reasoning(item: Extract<TranscriptRenderItem, { kind: "reasoning_trace" }>): Output;
  notice(
    item: Extract<TranscriptRenderItem, { kind: "notice" | "reviewer_feedback" | "reviewer_error" }>,
  ): Output;
  thinkingStatus(item: Extract<TranscriptRenderItem, { kind: "thinking_status" }>): Output;
}>;

export function locatorKey(row: CommittedRow): string {
  return `${String(row.Locator.event_sequence)}:${String(row.Locator.row_ordinal)}`;
}

function required<Value>(value: Value | null): Value {
  if (value === null) throw new ContractError("Transcript row kind must match its payload.");
  return value;
}

export function committedItem(row: CommittedRow, key = locatorKey(row)): TranscriptCommittedItem {
  const payloads = [
    row.User,
    row.Assistant,
    row.Tool,
    row.ReasoningTrace,
    row.Notice,
    row.ReviewerFeedback,
    row.ReviewerError,
  ];
  if (payloads.filter((payload) => payload !== null).length !== 1) {
    throw new ContractError("Transcript row must contain exactly one payload.");
  }
  const base = { state: "committed", key, row } as const;
  switch (row.Kind) {
    case "user":
      return { ...base, kind: row.Kind, value: required(row.User) };
    case "assistant":
      return { ...base, kind: row.Kind, value: required(row.Assistant) };
    case "tool":
      return { ...base, kind: row.Kind, value: required(row.Tool) };
    case "reasoning_trace":
      return { ...base, kind: row.Kind, value: required(row.ReasoningTrace) };
    case "notice":
      return { ...base, kind: row.Kind, value: required(row.Notice) };
    case "reviewer_feedback":
      return { ...base, kind: row.Kind, value: required(row.ReviewerFeedback) };
    case "reviewer_error":
      return { ...base, kind: row.Kind, value: required(row.ReviewerError) };
  }
}

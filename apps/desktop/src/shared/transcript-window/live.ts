import { ContractError } from "@/api";

import {
  committedItem,
  locatorKey,
  type TranscriptProvisionalItem,
  type TranscriptRenderItem,
} from "./renderSlots";
import { residentRows } from "./segments";
import type { CommittedRow, Hydration, ResidentSegments, TranscriptLiveFact } from "./types";

type Reasoning = Hydration["ActiveReasoningTraces"][number];

function reasoningKey(stepID: string, identity: Reasoning["Identity"]): string {
  const provider = identity.Provider;
  const kent = identity.Kent;
  if ((provider != null) === (kent != null)) {
    throw new ContractError("Reasoning correlation must identify exactly one provider or Kent trace.");
  }
  return provider != null
    ? JSON.stringify(["reasoning", stepID, "provider", provider.ItemID, provider.SummaryIndex])
    : JSON.stringify(["reasoning", stepID, "kent", kent]);
}

function assistantKey(streamID: string): string {
  return JSON.stringify(["assistant", streamID]);
}

function toolKey(toolCallID: string): string {
  return JSON.stringify(["tool", toolCallID]);
}

export function committedCorrelation(row: CommittedRow): string | null {
  const item = committedItem(row);
  switch (item.kind) {
    case "assistant":
      return item.value.StreamID != null ? assistantKey(item.value.StreamID) : null;
    case "tool":
      return toolKey(item.value.ToolCallID);
    case "reasoning_trace":
      return item.value.ProvisionalIdentity != null
        ? reasoningKey(item.value.StepID, item.value.ProvisionalIdentity)
        : null;
    case "user":
    case "notice":
    case "reviewer_feedback":
    case "reviewer_error":
      return null;
  }
}

function assistant(value: NonNullable<Hydration["ActiveAssistant"]>): TranscriptProvisionalItem {
  return { kind: "assistant", state: "live", key: assistantKey(value.StreamID), value };
}

function tool(value: Hydration["InFlightTools"][number]): TranscriptProvisionalItem {
  return { kind: "tool", state: "live", key: toolKey(value.ToolCallID), value };
}

function reasoning(value: Reasoning): TranscriptProvisionalItem {
  return { kind: "reasoning_trace", state: "live", key: reasoningKey(value.StepID, value.Identity), value };
}

function thinking(value: NonNullable<Hydration["ActiveThinkingStatus"]>): TranscriptProvisionalItem {
  return { kind: "thinking_status", key: JSON.stringify(["thinking", value.StepID]), value };
}

export function hydratedLive(hydration: Hydration): readonly TranscriptProvisionalItem[] {
  const items = [
    ...hydration.ActiveReasoningTraces.map(reasoning),
    ...(hydration.ActiveAssistant === null ? [] : [assistant(hydration.ActiveAssistant)]),
    ...hydration.InFlightTools.map(tool),
    ...(hydration.ActiveThinkingStatus === null ? [] : [thinking(hydration.ActiveThinkingStatus)]),
  ];
  if (new Set(items.map((item) => item.key)).size !== items.length) {
    throw new ContractError("Hydration contains duplicate live correlations.");
  }
  return items;
}

export function reduceLive(
  items: readonly TranscriptProvisionalItem[],
  fact: TranscriptLiveFact,
): readonly TranscriptProvisionalItem[] {
  let next: TranscriptProvisionalItem;
  switch (fact.kind) {
    case "assistant_delta":
      next = appendAssistantDelta(items, fact.payload);
      break;
    case "tool_start":
      next = tool(fact.payload);
      break;
    case "reasoning_trace_update":
      next = reasoning(fact.payload);
      break;
    case "thinking_status_update":
      next = thinking(fact.payload);
      break;
    case "assistant_stream_abort":
      return items.filter((item) => item.key !== assistantKey(fact.payload.StreamID));
    case "tool_abort":
      return items.filter((item) => item.key !== toolKey(fact.payload.ToolCallID));
    case "reasoning_trace_reset":
      return items.filter(
        (item) => item.kind !== "reasoning_trace" || item.value.StepID !== fact.payload.StepID,
      );
    case "step_state":
      return fact.payload.Lifecycle === "finished"
        ? items.filter((item) => item.value.StepID !== fact.payload.StepID)
        : items;
  }
  return updateLiveItem(items, next);
}

function appendAssistantDelta(
  items: readonly TranscriptProvisionalItem[],
  payload: Extract<TranscriptLiveFact, { kind: "assistant_delta" }>["payload"],
): TranscriptProvisionalItem {
  const { Delta, ...facts } = payload;
  const previous = items.find((item) => item.key === assistantKey(facts.StreamID));
  if (previous !== undefined && (previous.kind !== "assistant" || previous.value.StepID !== facts.StepID)) {
    throw new ContractError("Assistant delta does not match the live stream facts.");
  }
  return assistant({ ...facts, Text: (previous?.value.Text ?? "") + Delta });
}

function updateLiveItem(
  items: readonly TranscriptProvisionalItem[],
  next: TranscriptProvisionalItem,
): readonly TranscriptProvisionalItem[] {
  const previous = items.find((item) => item.key === next.key);
  if (previous === undefined) return [...items, next];
  if (previous.value.StepID !== next.value.StepID) {
    throw new ContractError("Live correlation changed its owning Step.");
  }
  return items.map((item) => (item.key === next.key ? next : item));
}

/** Presentation identity is retained only for resident rows and current provisional correlations. */
export function present(
  {
    segments,
    pool,
    provisional,
  }: Readonly<{
    segments: ResidentSegments;
    pool: readonly CommittedRow[];
    provisional: readonly TranscriptProvisionalItem[];
  }>,
  previous: readonly TranscriptRenderItem[],
  admitted: readonly CommittedRow[] = [],
): Readonly<{ items: readonly TranscriptRenderItem[]; provisional: readonly TranscriptProvisionalItem[] }> {
  const rows = residentRows(segments);
  const priorKeys = new Map(
    previous.flatMap((item) => ("row" in item ? [[locatorKey(item.row), item.key]] : [])),
  );
  const liveKeys = new Set(provisional.map((item) => item.key));
  const promotions = new Map<string, string>();
  const committedCorrelations = new Set<string>();
  for (const row of [...rows, ...pool, ...admitted]) {
    const key = committedCorrelation(row);
    if (key === null) continue;
    committedCorrelations.add(key);
    if (liveKeys.has(key)) promotions.set(locatorKey(row), key);
  }
  const remaining = provisional.filter((item) => !committedCorrelations.has(item.key));
  const items: TranscriptRenderItem[] = rows
    .map((row) => committedItem(row, priorKeys.get(locatorKey(row)) ?? promotions.get(locatorKey(row))))
    .filter((item) => item.row.Visibility !== "hidden");
  const tail = segments.at(-1);
  if (tail !== undefined && !tail.hasMoreBelow) items.push(...remaining);
  if (new Set(items.map((item) => item.key)).size !== items.length) {
    throw new ContractError("Transcript rows compete for one live presentation identity.");
  }
  return { items, provisional: remaining };
}

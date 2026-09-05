import type { ChatTranscriptPage, ChatTranscriptPayloadByKind, ContractError } from "@/api";

import type { TranscriptRenderItem } from "./renderSlots";

export type CommittedRow = ChatTranscriptPayloadByKind["committed_row"];
export type Hydration = ChatTranscriptPayloadByKind["hydration"];
export type RuntimeActivity = ChatTranscriptPayloadByKind["runtime_read_model_update"]["Activity"];
export type CompactionStatus = ChatTranscriptPayloadByKind["compaction_status"];
type LiveFactKind =
  | "assistant_delta"
  | "assistant_stream_abort"
  | "tool_start"
  | "tool_abort"
  | "reasoning_trace_update"
  | "reasoning_trace_reset"
  | "thinking_status_update"
  | "step_state";
export type TranscriptLiveFact = {
  [Kind in LiveFactKind]: Readonly<{ kind: Kind; payload: ChatTranscriptPayloadByKind[Kind] }>;
}[LiveFactKind];
export type Segment = Pick<
  ChatTranscriptPage,
  "entries" | "olderCursor" | "hasMoreAbove" | "newerCursor" | "hasMoreBelow"
>;
export type ResidentSegments = readonly [] | readonly [Segment] | readonly [Segment, Segment];
export type TranscriptDirection = "older" | "newer";
/** Reducer-owned edge admission, independent of the physical request and resident membership. */
export type TranscriptPageRequest = Readonly<{
  admission: symbol;
  direction: TranscriptDirection;
  cursor: number;
}>;
export type TranscriptWindowInput =
  | Readonly<{ kind: "committed-row"; row: CommittedRow }>
  | Readonly<{ kind: "live-fact"; fact: TranscriptLiveFact }>
  | Readonly<{ kind: "runtime-activity"; activity: RuntimeActivity }>
  | Readonly<{ kind: "compaction-status"; status: CompactionStatus }>
  | Readonly<{ kind: "opening-success"; permit: symbol; page: ChatTranscriptPage }>
  | Readonly<{ kind: "opening-failure"; permit: symbol; error: Error }>
  | Readonly<{
      kind: "initial-hydration" | "scratch-hydration" | "reattachment-hydration";
      hydration: Hydration;
    }>
  // Direction is the latest deliberate travel direction. Only a new deliberate edge visit emits this
  // input, never layout or a settled request. Visits during a pending read are consumed, not queued.
  | Readonly<{ kind: "edge-visit"; direction: TranscriptDirection; older: boolean; newer: boolean }>
  | Readonly<{ kind: "page-success"; request: TranscriptPageRequest; page: ChatTranscriptPage }>
  | Readonly<{ kind: "page-failure"; request: TranscriptPageRequest; error: Error }>
  // The external request owner has already admitted this bounded tail.
  | Readonly<{ kind: "replace-window"; page: ChatTranscriptPage }>
  | Readonly<{ kind: "retry"; direction: TranscriptDirection }>
  | Readonly<{ kind: "dispose" }>;
export type TranscriptBoundary =
  | Readonly<{ kind: "idle"; cursor: number | null }>
  | Readonly<{ kind: "loading"; cursor: number }>
  | Readonly<{ kind: "error"; cursor: number; error: Error }>;
export type TranscriptWindowSnapshot = Readonly<{
  items: readonly TranscriptRenderItem[];
  older: TranscriptBoundary;
  newer: TranscriptBoundary;
  opening: Readonly<{ kind: "loading" | "ready" | "disposed" }> | Readonly<{ kind: "error"; error: Error }>;
}>;
export type TranscriptWindowEffect =
  | Readonly<{ kind: "scratch-rehydration" }>
  | Readonly<{ kind: "page-request"; request: TranscriptPageRequest }>
  | Readonly<{ kind: "opening-failed"; error: Error }>;
export type TranscriptWindowResult =
  | Readonly<{ kind: "accepted"; effects: readonly TranscriptWindowEffect[] }>
  | Readonly<{ kind: "obsolete" | "disposed"; effects: readonly [] }>
  // The host routes this to its development fail-fast / controlled release-error boundary.
  | Readonly<{ kind: "contract-failure"; error: ContractError; effects: readonly [] }>;

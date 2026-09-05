import type { ChatTranscriptPage, ChatTranscriptPayloadByKind } from "@/api";

import type { TranscriptWindow, TranscriptPageRequest } from "./index";

export function row(sequence: number): ChatTranscriptPayloadByKind["committed_row"] {
  return {
    Kind: "user",
    Locator: { event_sequence: sequence, row_ordinal: 1 },
    Visibility: "ongoing",
    Integrity: 0,
    User: { Text: `Message ${String(sequence)}` },
    Assistant: null,
    Tool: null,
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: null,
    ReviewerError: null,
  };
}

export function page(
  entries: ChatTranscriptPage["entries"],
  olderCursor: number | null,
  newerCursor: number | null = null,
): ChatTranscriptPage {
  return {
    sessionID: "session",
    sessionName: null,
    conversationFreshness: 0,
    latestRollbackCandidate: null,
    entries,
    olderCursor,
    newerCursor,
    hasMoreAbove: olderCursor !== null,
    hasMoreBelow: newerCursor !== null,
  };
}

export function visit(window: TranscriptWindow, direction: "older" | "newer"): TranscriptPageRequest {
  const effect = window.dispatch({ kind: "edge-visit", direction, older: true, newer: true }).effects[0];
  if (effect?.kind !== "page-request") throw new Error("Expected a page request.");
  return effect.request;
}

export function sequences(window: TranscriptWindow): number[] {
  return window.snapshot.items.flatMap((item) => ("row" in item ? [item.row.Locator.event_sequence] : []));
}

export const idle: ChatTranscriptPayloadByKind["runtime_read_model_update"]["Activity"] = {
  State: "registered_idle",
  ActiveStep: null,
  Reviewer: "inactive",
  QueueAccepting: true,
  DiagnosticRecovery: false,
};

export function hydration(
  entries: ChatTranscriptPage["entries"],
  count = 0,
  activity = idle,
  compaction: ChatTranscriptPayloadByKind["compaction_status"] | null = null,
): ChatTranscriptPayloadByKind["hydration"] {
  return {
    SessionIdentity: {
      SessionID: "session",
      SessionName: null,
      ConversationFreshness: 0,
      ExecutionTarget: null,
    },
    SessionStatus: {
      ReviewerFrequency: "off",
      ReviewerEnabled: false,
      AutoCompactionEnabled: true,
      QuestionsEnabled: true,
      FastModeAvailable: false,
      FastModeEnabled: false,
      ThinkingLevel: "medium",
      CompactionMode: "local",
      CompactionCount: count,
      Workflow: null,
    },
    RuntimeReadModelUpdate: {
      Version: { Epoch: "epoch", Generation: 1, Sequence: 1 },
      Activity: activity,
    },
    TailSegment: { Entries: entries, OlderCursor: 300, HasMoreAbove: true },
    ActiveAssistant: null,
    ActiveThinkingStatus: null,
    ActiveReasoningTraces: [],
    InFlightTools: [],
    ActiveStep: null,
    ActiveCompaction: compaction,
    PendingPrompts: [],
    BackgroundActivities: [],
    ContextUsage: null,
    GoalStatus: null,
  };
}

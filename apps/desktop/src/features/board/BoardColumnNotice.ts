import { errorMessage } from "@/api";
import type { StatusNotice } from "@/ui";
import type { BoardColumnNoticeEvent } from "./BoardColumnDataOwner";

export type BoardColumnNoticeCopy = Readonly<{
  cardsLoadRetryBody: string;
  cardsLoadFailed: string;
  retry: string;
}>;

export type BoardColumnNoticeDiagnostic = Readonly<{
  message: string;
  context: Readonly<Record<string, string>>;
}>;

export function boardColumnNoticeDiagnostic(
  event: BoardColumnNoticeEvent,
  scope: Readonly<{ projectID: string; workflowID: string }>,
): BoardColumnNoticeDiagnostic | null {
  if (event.kind === "dismiss") {
    return null;
  }
  return {
    message: "Board task-card load failed.",
    context: {
      columnID: event.columnID,
      error: errorMessage(event.error),
      filterGeneration: event.generation.toString(),
      projectID: scope.projectID,
      workflowID: scope.workflowID,
    },
  };
}

export function boardColumnNoticeStatusNotice(
  event: BoardColumnNoticeEvent,
  copy: BoardColumnNoticeCopy,
): StatusNotice | null {
  if (event.kind === "dismiss") {
    return null;
  }
  return {
    actionLabel: copy.retry,
    body: copy.cardsLoadRetryBody,
    id: event.noticeID,
    onAction: event.retry,
    title: copy.cardsLoadFailed,
    tone: "danger",
    durationMs: Infinity,
  };
}

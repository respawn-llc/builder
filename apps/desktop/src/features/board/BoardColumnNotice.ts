import type { StatusNotice } from "@/ui";
import { errorMessage } from "@/api";
import type { BoardColumnNoticeEvent } from "./BoardColumnDataOwner";

export type BoardColumnNoticeCopy = Readonly<{
  cardsLoadFailed: string;
  retry: string;
}>;

export function boardColumnNoticeStatusNotice(
  event: BoardColumnNoticeEvent,
  copy: BoardColumnNoticeCopy,
): StatusNotice | null {
  if (event.kind === "dismiss") {
    return null;
  }
  return {
    actionLabel: copy.retry,
    body: errorMessage(event.error),
    id: event.noticeID,
    onAction: event.retry,
    title: copy.cardsLoadFailed,
    tone: "danger",
    durationMs: Infinity,
  };
}

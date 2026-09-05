import type { ChatTranscriptCommittedRow } from "@/api";

import { TranscriptNoticeRow } from "./TranscriptNoticeRow";
import { TranscriptReviewerRow } from "./TranscriptReviewerRow";

export function TranscriptNoticeReviewerSlot({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  if (row.Kind === "notice") return <TranscriptNoticeRow row={row} />;
  if (row.Kind === "reviewer_feedback" || row.Kind === "reviewer_error") {
    return <TranscriptReviewerRow row={row} />;
  }
  return null;
}

import { useTranslation } from "react-i18next";

import type { ChatTranscriptCommittedRow } from "@/api";
import { StaticMarkdown } from "@/ui";

import { TranscriptContentBody, TranscriptFlatRow } from "./TranscriptFlatRow";
import { projectTranscriptRow } from "./transcriptRowProjector";

export function TranscriptReviewerRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  const { t } = useTranslation();
  if (row.Kind !== "reviewer_feedback" && row.Kind !== "reviewer_error") return null;

  const projection = projectTranscriptRow(row, {
    reviewerFeedbackCompactText: (count) => t("chatTranscript.reviewerSuggestions", { count }),
    structuredNoticeCompactText: () => "",
  });
  if (projection === null) return null;

  const formatter = { structuredNoticeText: () => "" };
  const body =
    projection.body.kind === "reviewer_feedback" ? (
      <div className="chat-transcript-reviewer-suggestions">
        {projection.body.suggestions.map((suggestion, index) => (
          <div className="chat-transcript-reviewer-suggestion" key={`${String(index + 1)}-${suggestion}`}>
            <span className="chat-transcript-reviewer-number">{index + 1}.</span>
            <StaticMarkdown value={suggestion} />
          </div>
        ))}
      </div>
    ) : (
      <TranscriptContentBody content={projection.body} formatter={formatter} />
    );
  return (
    <TranscriptFlatRow
      body={body}
      formatter={formatter}
      labels={{
        collapseLabel: t("app.collapse"),
        copyFailedLabel: t("chatTranscript.copyFailed"),
        copyLabel: t("chatTranscript.copy"),
        copiedLabel: t("chatTranscript.copied"),
        expandLabel: t("app.expand"),
      }}
      projection={projection}
    />
  );
}

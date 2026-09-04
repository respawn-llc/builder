import { useTranslation } from "react-i18next";
import { CircleX, MessageCircle } from "lucide-react";

import type { ChatTranscriptCommittedRow } from "@/api";
import { StaticMarkdown } from "@/ui";

import { reviewerFeedbackCopyText } from "./transcriptReviewerPolicy";
import { TranscriptFlatRow } from "./TranscriptFlatRow";

export function TranscriptReviewerRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  const { t } = useTranslation();
  if (row.Kind !== "reviewer_feedback" && row.Kind !== "reviewer_error") return null;
  if (row.Visibility === "hidden") return null;

  const labels = {
    collapseLabel: t("app.collapse"),
    copyFailedLabel: t("chatTranscript.copyFailed"),
    copyLabel: t("chatTranscript.copy"),
    copiedLabel: t("chatTranscript.copied"),
    expandLabel: t("app.expand"),
  };
  if (row.Kind === "reviewer_feedback") {
    if (row.ReviewerFeedback === null) throw new Error("Reviewer feedback row is missing its payload.");
    const suggestions = [...row.ReviewerFeedback.Suggestions];
    return (
      <TranscriptFlatRow
        body={
          <div className="chat-transcript-reviewer-suggestions">
            {suggestions.map((suggestion, index) => (
              <div className="chat-transcript-reviewer-suggestion" key={`${String(index + 1)}-${suggestion}`}>
                <span className="chat-transcript-reviewer-number">{index + 1}.</span>
                <StaticMarkdown value={suggestion} />
              </div>
            ))}
          </div>
        }
        copyText={reviewerFeedbackCopyText(suggestions)}
        defaultExpanded={false}
        icon={<MessageCircle className="size-4" />}
        iconTone="neutral"
        labels={labels}
        summary={t("chatTranscript.reviewerSuggestions", { count: row.ReviewerFeedback.SuggestionCount })}
      />
    );
  }
  if (row.ReviewerError === null) throw new Error("Reviewer error row is missing its payload.");
  return (
    <TranscriptFlatRow
      body={<p className="chat-transcript-row-body">{row.ReviewerError.Detail}</p>}
      copyText={row.ReviewerError.Detail}
      defaultExpanded
      icon={<CircleX className="size-4" />}
      iconTone="error"
      labels={labels}
      summary={row.ReviewerError.Detail}
    />
  );
}

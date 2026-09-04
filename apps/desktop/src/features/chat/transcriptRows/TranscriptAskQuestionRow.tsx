import { Circle, CircleDot, CornerDownRight, Star } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ChatTranscriptCommittedRow } from "@/api";
import { StaticMarkdown } from "@/ui";

import { TranscriptFlatRow } from "./TranscriptFlatRow";
import { projectTranscriptRow } from "./transcriptRowProjector";

export function TranscriptAskQuestionRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  const { t } = useTranslation();
  if (row.Kind !== "tool" || row.Tool?.Presentation?.Presentation !== "ask_question") return null;

  const projection = projectTranscriptRow(row, {
    reviewerFeedbackCompactText: (count) => t("chatTranscript.reviewerSuggestions", { count }),
    structuredNoticeCompactText: () => "",
  });
  if (projection?.body.kind !== "ask_question") return null;

  const formatter = { structuredNoticeText: () => "" };
  return (
    <TranscriptFlatRow
      body={<AskQuestionBody presentation={projection.body.presentation} tool={projection.body.tool} />}
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

function AskQuestionBody({
  presentation,
  tool,
}: Readonly<{
  tool: NonNullable<ChatTranscriptCommittedRow["Tool"]>;
  presentation: NonNullable<NonNullable<ChatTranscriptCommittedRow["Tool"]>["Presentation"]>;
}>) {
  const answer = tool.QuestionAnswer;
  if (!tool.IsError && (answer === undefined || answer === null)) {
    throw new Error("Answered Ask Question row is missing its typed answer.");
  }
  const selectedOptionNumber = tool.IsError ? null : (answer?.SelectedOptionNumber ?? null);
  return (
    <div className="chat-transcript-question-body">
      <StaticMarkdown value={presentation.Question} />
      <QuestionOptions
        recommendedOptionIndex={presentation.RecommendedOptionIndex}
        selectedOptionNumber={selectedOptionNumber}
        suggestions={presentation.Suggestions}
      />
      <QuestionResponse answer={answer} isError={tool.IsError} text={tool.Text} />
    </div>
  );
}

function QuestionResponse({
  answer,
  isError,
  text,
}: Readonly<{
  answer: NonNullable<NonNullable<ChatTranscriptCommittedRow["Tool"]>["QuestionAnswer"]> | null | undefined;
  isError: boolean;
  text: string;
}>) {
  if (isError) {
    return <p className="chat-transcript-row-body chat-transcript-question-error">{text}</p>;
  }
  if (answer?.Freeform === undefined || answer.Freeform === null) return null;
  return <QuestionAnswerText text={answer.Freeform} />;
}

function QuestionOptions({
  recommendedOptionIndex,
  selectedOptionNumber,
  suggestions,
}: Readonly<{
  recommendedOptionIndex: number;
  selectedOptionNumber: number | null;
  suggestions: readonly string[];
}>) {
  if (suggestions.length === 0) return null;
  return (
    <div className="chat-transcript-question-options">
      {suggestions.map((suggestion, index) => {
        const optionNumber = index + 1;
        const selected = selectedOptionNumber === optionNumber;
        const recommended = recommendedOptionIndex === optionNumber;
        return (
          <div
            className={
              selected
                ? "chat-transcript-question-option chat-transcript-question-option--selected"
                : "chat-transcript-question-option"
            }
            key={`${String(optionNumber)}-${suggestion}`}
          >
            {selected ? (
              <CircleDot aria-hidden="true" className="size-4 shrink-0" />
            ) : (
              <Circle aria-hidden="true" className="size-4 shrink-0" />
            )}
            <span className="shrink-0">{optionNumber}.</span>
            <div className="min-w-0 flex-1">
              <StaticMarkdown value={suggestion} />
            </div>
            {recommended ? <Star aria-hidden="true" className="size-3 shrink-0 fill-current" /> : null}
          </div>
        );
      })}
    </div>
  );
}

function QuestionAnswerText({ text }: Readonly<{ text: string }>) {
  return (
    <div className="chat-transcript-question-answer">
      <CornerDownRight aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <span className="whitespace-pre-wrap break-words select-text">{text}</span>
    </div>
  );
}

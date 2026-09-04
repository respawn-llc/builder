import { Circle, CircleAlert, CircleDot, CircleX, CornerDownRight, Star } from "lucide-react";

import type { ChatTranscriptCommittedRow } from "@/api";
import { StaticMarkdown } from "@/ui";

import { TranscriptFlatRow } from "./TranscriptFlatRow";
import {
  askQuestionCopyText,
  askQuestionSummary,
  isAskQuestionToolRow,
  type TranscriptTool,
  type TranscriptToolPresentation,
} from "./transcriptAskQuestionPolicy";

export function TranscriptAskQuestionRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  if (!isAskQuestionToolRow(row) || row.Visibility === "hidden") return null;
  const tool = row.Tool;
  const presentation = tool.Presentation;
  return (
    <TranscriptFlatRow
      body={<AskQuestionBody presentation={presentation} tool={tool} />}
      copyText={askQuestionCopyText(row)}
      defaultExpanded={!tool.IsError}
      icon={<AskQuestionIcon isError={tool.IsError} />}
      iconTone={tool.IsError ? "error" : "success"}
      summary={askQuestionSummary(row)}
    />
  );
}

function AskQuestionIcon({ isError }: Readonly<{ isError: boolean }>) {
  const Icon = isError ? CircleX : CircleAlert;
  return <Icon className="size-4" />;
}

function AskQuestionBody({
  presentation,
  tool,
}: Readonly<{
  tool: TranscriptTool;
  presentation: TranscriptToolPresentation;
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
        isError={tool.IsError}
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
  isError,
  recommendedOptionIndex,
  selectedOptionNumber,
  suggestions,
}: Readonly<{
  isError: boolean;
  recommendedOptionIndex: number;
  selectedOptionNumber: number | null;
  suggestions: readonly string[];
}>) {
  if ((!isError && selectedOptionNumber === null) || suggestions.length === 0) return null;
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
            {selected ? <CircleDot className="size-4 shrink-0" /> : <Circle className="size-4 shrink-0" />}
            <span className="shrink-0">{optionNumber}.</span>
            <div className="min-w-0 flex-1">
              <StaticMarkdown value={suggestion} />
            </div>
            {recommended ? <Star className="size-3 shrink-0 fill-current" /> : null}
          </div>
        );
      })}
    </div>
  );
}

function QuestionAnswerText({ text }: Readonly<{ text: string }>) {
  return (
    <div className="chat-transcript-question-answer">
      <CornerDownRight className="mt-0.5 size-4 shrink-0" />
      <span className="whitespace-pre-wrap break-words select-text">{text}</span>
    </div>
  );
}

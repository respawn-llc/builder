import type { ChatTranscriptCommittedRow } from "@/api";
import { firstPresent } from "@/shared/text";

export type TranscriptTool = NonNullable<ChatTranscriptCommittedRow["Tool"]>;
export type TranscriptToolPresentation = NonNullable<TranscriptTool["Presentation"]>;
type TranscriptAskQuestionPresentation = Omit<TranscriptToolPresentation, "Presentation"> &
  Readonly<{ Presentation: "ask_question" }>;

export type TranscriptAskQuestionToolRow = Omit<ChatTranscriptCommittedRow, "Kind" | "Tool"> &
  Readonly<{
    Kind: "tool";
    Tool: Omit<TranscriptTool, "Presentation"> &
      Readonly<{ Presentation: TranscriptAskQuestionPresentation }>;
  }>;

export function isAskQuestionToolRow(row: ChatTranscriptCommittedRow): row is TranscriptAskQuestionToolRow {
  return row.Kind === "tool" && row.Tool?.Presentation?.Presentation === "ask_question";
}

export function askQuestionSummary(row: TranscriptAskQuestionToolRow): string {
  return (
    firstPresent(row.Tool.Presentation.CompactText, row.Tool.Presentation.Question) ??
    row.Tool.Presentation.Question
  );
}

export function askQuestionCopyText(row: TranscriptAskQuestionToolRow): string {
  const tool = row.Tool;
  const presentation = tool.Presentation;
  if (tool.IsError) return [presentation.Question, tool.Text].join("\n\n");
  const answer = tool.QuestionAnswer;
  if (answer === undefined || answer === null) {
    throw new Error("Answered Ask Question content is missing its typed answer.");
  }
  const sections = [presentation.Question];
  if (answer.SelectedOptionNumber !== undefined && answer.SelectedOptionNumber !== null) {
    const selected = presentation.Suggestions[answer.SelectedOptionNumber - 1];
    if (selected !== undefined) sections.push(selected);
  }
  if (answer.Freeform !== undefined && answer.Freeform !== null) sections.push(answer.Freeform);
  return sections.join("\n\n");
}

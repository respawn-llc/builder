import {
  ContractError,
  resolveChatToolIdentity,
  type ChatTranscriptCommittedRow,
  type ChatTranscriptPayloadByKind,
} from "@/api";

type TranscriptToolStart = ChatTranscriptPayloadByKind["tool_start"];
type TranscriptTool = NonNullable<ChatTranscriptCommittedRow["Tool"]>;
type TranscriptToolPresentation = NonNullable<TranscriptTool["Presentation"]>;

export type TranscriptNonQuestionToolPresentation = Omit<TranscriptToolPresentation, "Presentation"> &
  Readonly<{ Presentation: "default" | "shell" }>;

export type TranscriptLiveToolRow = Omit<TranscriptToolStart, "Presentation"> &
  Readonly<{ Presentation?: TranscriptNonQuestionToolPresentation | null }>;

export type TranscriptCommittedToolRow = Omit<ChatTranscriptCommittedRow, "Kind" | "Tool"> &
  Readonly<{
    Kind: "tool";
    Tool: Omit<TranscriptTool, "Presentation"> &
      Readonly<{ Presentation?: TranscriptNonQuestionToolPresentation | null }>;
  }>;

export type TranscriptToolSlotCandidate =
  | Readonly<{ kind: "live"; tool: TranscriptToolStart }>
  | Readonly<{
      kind: "committed";
      row: Omit<ChatTranscriptCommittedRow, "Kind" | "Tool"> &
        Readonly<{ Kind: "tool"; Tool: TranscriptTool }>;
    }>;

declare const transcriptToolSlotItemBrand: unique symbol;

export type TranscriptToolSlotItem = (
  | Readonly<{ kind: "live"; tool: TranscriptLiveToolRow }>
  | Readonly<{ kind: "committed"; row: TranscriptCommittedToolRow }>
) &
  Readonly<{ [transcriptToolSlotItemBrand]: true }>;

export function transcriptToolSlotItem(candidate: TranscriptToolSlotCandidate): TranscriptToolSlotItem {
  assertTranscriptToolSlotItem(candidate);
  return candidate;
}

function assertTranscriptToolSlotItem(
  candidate: TranscriptToolSlotCandidate,
): asserts candidate is TranscriptToolSlotItem {
  const tool = candidate.kind === "live" ? candidate.tool : candidate.row.Tool;
  const presentation = tool.Presentation;
  if (
    resolveChatToolIdentity(tool.ToolName)?.kind === "ask-question" ||
    !isNonQuestionPresentation(presentation)
  ) {
    throw new ContractError("Ask Question cannot be rendered by the non-Question tool slot", [
      { code: "invalid_tool_slot", path: ["ToolName"] },
    ]);
  }
}

function isNonQuestionPresentation(
  presentation: TranscriptToolPresentation | null | undefined,
): presentation is TranscriptNonQuestionToolPresentation | null | undefined {
  if (presentation === null || presentation === undefined) return true;
  return (
    presentation.Presentation !== "ask_question" &&
    presentation.RenderBehavior !== "ask_question" &&
    presentation.Question.trim().length === 0 &&
    presentation.Suggestions.length === 0 &&
    presentation.RecommendedOptionIndex === 0
  );
}

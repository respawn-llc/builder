import type { ChatTranscriptCommittedRow, ChatTranscriptPayloadByKind } from "@/api";

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

export type TranscriptToolSlotItem =
  | Readonly<{ kind: "live"; tool: TranscriptLiveToolRow }>
  | Readonly<{ kind: "committed"; row: TranscriptCommittedToolRow }>;

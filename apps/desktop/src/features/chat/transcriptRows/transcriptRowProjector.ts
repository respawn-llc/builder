import type { ChatTranscriptCommittedRow } from "@/api";

export type TranscriptNotice = NonNullable<ChatTranscriptCommittedRow["Notice"]>;
export type TranscriptTool = NonNullable<ChatTranscriptCommittedRow["Tool"]>;
export type TranscriptToolPresentation = NonNullable<TranscriptTool["Presentation"]>;
type TranscriptAskQuestionPresentation = Omit<TranscriptToolPresentation, "Presentation"> &
  Readonly<{ Presentation: "ask_question" }>;
type VisibleTranscriptRowVisibility = Exclude<ChatTranscriptCommittedRow["Visibility"], "hidden">;

export type TranscriptAskQuestionToolRow = Omit<ChatTranscriptCommittedRow, "Visibility" | "Kind" | "Tool"> &
  Readonly<{
    Visibility: VisibleTranscriptRowVisibility;
    Kind: "tool";
    Tool: Omit<TranscriptTool, "Presentation"> &
      Readonly<{ Presentation: TranscriptAskQuestionPresentation }>;
  }>;

export type TranscriptRowIcon =
  | "notice"
  | "notice_error"
  | "notice_warning"
  | "notice_compaction"
  | "notice_worktree"
  | "notice_cache"
  | "notice_repair"
  | "notice_provider"
  | "reviewer_feedback"
  | "reviewer_error"
  | "ask_question"
  | "ask_question_error";

export type TranscriptRowIconTone = "neutral" | "warning" | "error" | "success";

export type TranscriptRowContent =
  | Readonly<{ kind: "markdown"; text: string }>
  | Readonly<{ kind: "plain_text"; text: string }>
  | Readonly<{ kind: "structured_notice"; notice: TranscriptNotice }>
  | Readonly<{ kind: "reviewer_feedback"; suggestions: readonly string[] }>
  | Readonly<{ kind: "reviewer_error"; text: string }>
  | Readonly<{ kind: "ask_question"; tool: TranscriptTool; presentation: TranscriptToolPresentation }>;

export type TranscriptRowProjectorLabels = Readonly<{
  reviewerFeedbackCompactText(suggestionCount: number): string;
  structuredNoticeCompactText(notice: TranscriptNotice): string;
}>;

export type TranscriptRowContentFormatter = Readonly<{
  structuredNoticeText(notice: TranscriptNotice): string;
}>;

export type TranscriptRowProjection = Readonly<{
  compactText: string;
  icon: TranscriptRowIcon;
  iconTone: TranscriptRowIconTone;
  defaultExpanded: boolean;
  body: TranscriptRowContent;
  copySource: TranscriptRowContent;
}>;

const markdownMessageTypes = new Set([
  "agents.md",
  "skills",
  "subagents",
  "environment",
  "compaction_summary",
  "headless_mode",
  "headless_mode_exit",
  "workflow_mode",
  "active_goal_continuation",
  "agent_steer",
]);

const contextMessageTypes = new Set([
  ...markdownMessageTypes,
  "compaction_soon_reminder",
  "handoff_future_message",
  "manual_compaction_carryover",
  "workflow_mode_exit",
  "worktree_mode",
  "worktree_mode_exit",
  "session_rebind",
  "goal",
  "background_notice",
]);

export function projectTranscriptRow(
  row: ChatTranscriptCommittedRow,
  labels: TranscriptRowProjectorLabels,
): TranscriptRowProjection | null {
  switch (row.Kind) {
    case "notice":
      return projectNotice(row, labels);
    case "reviewer_feedback":
      return projectReviewerFeedback(row, labels);
    case "reviewer_error":
      return projectReviewerError(row);
    case "tool":
      return projectAskQuestion(row);
    case "user":
    case "assistant":
    case "reasoning_trace":
      return null;
    default:
      return null;
  }
}

export function isAskQuestionToolRow(row: ChatTranscriptCommittedRow): row is TranscriptAskQuestionToolRow {
  return (
    row.Visibility !== "hidden" &&
    row.Kind === "tool" &&
    row.Tool?.Presentation?.Presentation === "ask_question"
  );
}

export function transcriptRowContentText(
  content: TranscriptRowContent,
  formatter: TranscriptRowContentFormatter,
): string {
  switch (content.kind) {
    case "markdown":
    case "plain_text":
    case "reviewer_error":
      return content.text;
    case "structured_notice":
      return formatter.structuredNoticeText(content.notice);
    case "reviewer_feedback":
      return content.suggestions.map((suggestion, index) => `${String(index + 1)}. ${suggestion}`).join("\n");
    case "ask_question":
      return askQuestionCopyText(content.tool, content.presentation);
  }
}

function projectNotice(
  row: ChatTranscriptCommittedRow,
  labels: TranscriptRowProjectorLabels,
): TranscriptRowProjection | null {
  const notice = row.Notice;
  if (notice === null || shouldOmitNotice(row, notice)) {
    return null;
  }

  const body = isMarkdownNotice(notice)
    ? ({ kind: "markdown", text: noticeOriginalText(notice) } as const)
    : ({ kind: "structured_notice", notice } as const);
  const copySource =
    body.kind === "markdown" && notice.Reason === "compaction"
      ? ({ kind: "structured_notice", notice } as const)
      : body;
  return {
    compactText: noticeCompactText(notice, labels),
    icon: noticeIcon(notice),
    iconTone: noticeIconTone(notice),
    defaultExpanded: noticeDefaultExpanded(notice, row.Visibility),
    body,
    copySource,
  };
}

function shouldOmitNotice(row: ChatTranscriptCommittedRow, notice: TranscriptNotice): boolean {
  if (row.Visibility === "hidden") return true;
  const messageType = notice.MessageType ?? null;
  if (messageType === "interruption" || notice.Diagnostic?.Code === "reviewer_status") return true;
  return (
    isKnownDeveloperContext(notice) &&
    notice.Reason !== "compaction" &&
    noticeRawText(notice).trim().length === 0
  );
}

function projectReviewerFeedback(
  row: ChatTranscriptCommittedRow,
  labels: TranscriptRowProjectorLabels,
): TranscriptRowProjection {
  if (row.ReviewerFeedback === null) {
    throw new Error("Reviewer feedback row is missing its payload.");
  }
  const body = {
    kind: "reviewer_feedback",
    suggestions: [...row.ReviewerFeedback.Suggestions],
  } as const;
  return {
    compactText: labels.reviewerFeedbackCompactText(row.ReviewerFeedback.SuggestionCount),
    icon: "reviewer_feedback",
    iconTone: "neutral",
    defaultExpanded: false,
    body,
    copySource: body,
  };
}

function projectReviewerError(row: ChatTranscriptCommittedRow): TranscriptRowProjection {
  if (row.ReviewerError === null) {
    throw new Error("Reviewer error row is missing its payload.");
  }
  const body = { kind: "reviewer_error", text: row.ReviewerError.Detail } as const;
  return {
    compactText: row.ReviewerError.Detail,
    icon: "reviewer_error",
    iconTone: "error",
    defaultExpanded: true,
    body,
    copySource: body,
  };
}

function projectAskQuestion(row: ChatTranscriptCommittedRow): TranscriptRowProjection | null {
  if (!isAskQuestionToolRow(row)) {
    return null;
  }
  const presentation = row.Tool.Presentation;
  const body = { kind: "ask_question", tool: row.Tool, presentation } as const;
  return {
    compactText: firstPresent(presentation.CompactText, presentation.Question),
    icon: row.Tool.IsError ? "ask_question_error" : "ask_question",
    iconTone: row.Tool.IsError ? "error" : "success",
    defaultExpanded: !row.Tool.IsError,
    body,
    copySource: body,
  };
}

function isMarkdownNotice(notice: TranscriptNotice): boolean {
  if (notice.MessageType === "compaction_summary") {
    return (
      notice.Compaction?.Detail !== undefined &&
      notice.Compaction.Detail !== null &&
      notice.Compaction.Detail.trim().length > 0
    );
  }
  return (
    notice.MessageType !== undefined &&
    notice.MessageType !== null &&
    markdownMessageTypes.has(notice.MessageType)
  );
}

function isKnownDeveloperContext(notice: TranscriptNotice): boolean {
  return (
    notice.MessageType !== undefined &&
    notice.MessageType !== null &&
    contextMessageTypes.has(notice.MessageType)
  );
}

function noticeOriginalText(notice: TranscriptNotice): string {
  return firstPresent(
    notice.Compaction?.Detail,
    notice.Diagnostic?.Detail,
    notice.LegacyText,
    notice.CondensedText,
    notice.CompactLabel,
    notice.SourcePath,
    notice.Reason,
  );
}

function noticeRawText(notice: TranscriptNotice): string {
  return firstPresent(
    notice.Compaction?.Detail,
    notice.Diagnostic?.Detail,
    notice.LegacyText,
    notice.CondensedText,
    notice.CompactLabel,
    notice.SourcePath,
  );
}

function noticeCompactText(notice: TranscriptNotice, labels: TranscriptRowProjectorLabels): string {
  const typedText = firstPresent(
    notice.CompactLabel,
    notice.CondensedText,
    notice.LegacyText,
    notice.SourcePath,
  );
  if (typedText !== "") return typedText;
  return firstPresent(labels.structuredNoticeCompactText(notice), notice.Reason);
}

function noticeDefaultExpanded(
  notice: TranscriptNotice,
  visibility: ChatTranscriptCommittedRow["Visibility"],
): boolean {
  if (isKnownDeveloperContext(notice) || notice.Reason === "compaction") {
    return false;
  }
  if (notice.Diagnostic?.Code === "reviewer_suggestions") {
    return false;
  }
  if (notice.MessageType === "error_feedback") {
    return true;
  }
  if (isEmptyUnknownContext(notice, visibility)) return true;
  if (
    notice.MessageType === "runtime_diagnostic" ||
    (notice.Reason === "runtime_diagnostic" && notice.Severity !== "error")
  ) {
    return false;
  }
  return true;
}

function noticeIcon(notice: TranscriptNotice): TranscriptRowIcon {
  return firstIcon(
    notice.Severity === "error" ? "notice_error" : undefined,
    noticeMessageTypeIcon(notice),
    noticeReasonIcon(notice),
    noticeReviewerIcon(notice),
    notice.Severity === "warning" ? "notice_warning" : undefined,
    "notice",
  );
}

function noticeMessageTypeIcon(notice: TranscriptNotice): TranscriptRowIcon | undefined {
  switch (notice.MessageType) {
    case undefined:
    case null:
      return undefined;
    case "error_feedback":
      return "notice_error";
    case "compaction_soon_reminder":
      return "notice_warning";
    case "worktree_mode":
    case "worktree_mode_exit":
      return "notice_worktree";
    default:
      return undefined;
  }
}

function noticeReasonIcon(notice: TranscriptNotice): TranscriptRowIcon | undefined {
  switch (notice.Reason) {
    case "compaction":
      return "notice_compaction";
    case "cache_warning":
      return "notice_cache";
    case "tool_output_repair":
      return "notice_repair";
    case "provider_model_mismatch":
      return "notice_provider";
    case "legacy_untyped_notice":
    case "runtime_diagnostic":
      return undefined;
    default:
      return undefined;
  }
}

function noticeReviewerIcon(notice: TranscriptNotice): TranscriptRowIcon | undefined {
  switch (notice.Diagnostic?.Code) {
    case undefined:
      return undefined;
    case "reviewer_error":
      return "reviewer_error";
    case "reviewer_suggestions":
      return "reviewer_feedback";
    default:
      return undefined;
  }
}

function firstIcon(...icons: readonly (TranscriptRowIcon | undefined)[]): TranscriptRowIcon {
  for (const icon of icons) {
    if (icon !== undefined) return icon;
  }
  throw new Error("Transcript notice icon classification produced no icon.");
}

function noticeIconTone(notice: TranscriptNotice): TranscriptRowIconTone {
  if (
    notice.Severity === "error" ||
    notice.MessageType === "error_feedback" ||
    notice.Diagnostic?.Code === "reviewer_error"
  ) {
    return "error";
  }
  if (
    notice.Severity === "warning" ||
    notice.MessageType === "compaction_soon_reminder" ||
    notice.Reason === "cache_warning" ||
    notice.Reason === "tool_output_repair" ||
    notice.Reason === "provider_model_mismatch"
  ) {
    return "warning";
  }
  return "neutral";
}

function isEmptyUnknownContext(
  notice: TranscriptNotice,
  visibility: ChatTranscriptCommittedRow["Visibility"],
): boolean {
  const messageType = notice.MessageType;
  return (
    visibility === "detail" &&
    notice.Reason === "runtime_diagnostic" &&
    messageType !== undefined &&
    messageType !== null &&
    notice.Diagnostic?.Code === messageType &&
    !contextMessageTypes.has(messageType)
  );
}

export function firstPresent(...values: readonly (string | null | undefined)[]): string {
  for (const value of values) {
    if (value !== undefined && value !== null && value.trim().length > 0) {
      return value;
    }
  }
  return "";
}

function askQuestionCopyText(tool: TranscriptTool, presentation: TranscriptToolPresentation): string {
  if (tool.IsError) {
    return [presentation.Question, tool.Text].join("\n\n");
  }
  const answer = tool.QuestionAnswer;
  if (answer === undefined || answer === null) {
    throw new Error("Answered Ask Question content is missing its typed answer.");
  }
  const sections = [presentation.Question];
  if (answer.SelectedOptionNumber !== undefined && answer.SelectedOptionNumber !== null) {
    const selected = presentation.Suggestions[answer.SelectedOptionNumber - 1];
    if (selected !== undefined) sections.push(selected);
  }
  if (answer.Freeform !== undefined && answer.Freeform !== null) {
    sections.push(answer.Freeform);
  }
  return sections.join("\n\n");
}

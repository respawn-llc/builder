import {
  CircleX,
  Database,
  GitBranch,
  Info,
  RefreshCw,
  Server,
  TriangleAlert,
  Wrench,
  type LucideIcon,
} from "lucide-react";

import type { ChatTranscriptCommittedRow } from "@/api";

import type { TranscriptFlatRowIconTone } from "./TranscriptFlatRow";
import { firstPresent } from "./firstPresent";

export type TranscriptNotice = NonNullable<ChatTranscriptCommittedRow["Notice"]>;

export type TranscriptNoticeBody =
  Readonly<{ kind: "markdown"; text: string }> | Readonly<{ kind: "plain_text"; text: string }>;

export type TranscriptNoticePolicy = Readonly<{
  summary: string;
  icon: LucideIcon;
  iconTone: TranscriptFlatRowIconTone;
  defaultExpanded: boolean;
  body: TranscriptNoticeBody;
  copyText: string;
}>;

export type TranscriptNoticeProse = Readonly<{
  expanded: string;
  compact: string;
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

export function projectNotice(
  row: ChatTranscriptCommittedRow,
  prose: TranscriptNoticeProse,
): TranscriptNoticePolicy | null {
  const notice = row.Notice;
  if (notice === null || shouldOmitNotice(row, notice)) return null;

  const body = isMarkdownNotice(notice)
    ? ({ kind: "markdown", text: noticeOriginalText(notice) } as const)
    : ({ kind: "plain_text", text: prose.expanded } as const);
  const copyText = body.kind === "markdown" && notice.Reason !== "compaction" ? body.text : prose.expanded;
  return {
    summary: noticeCompactText(notice, prose.compact),
    icon: noticeIcon(notice),
    iconTone: noticeIconTone(notice),
    defaultExpanded: noticeDefaultExpanded(notice, row.Visibility),
    body,
    copyText,
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

function noticeCompactText(notice: TranscriptNotice, compactText: string): string {
  const typedText = firstPresent(
    notice.CompactLabel,
    notice.CondensedText,
    notice.LegacyText,
    notice.SourcePath,
  );
  if (typedText !== "") return typedText;
  return firstPresent(compactText, notice.Reason);
}

function noticeDefaultExpanded(
  notice: TranscriptNotice,
  visibility: ChatTranscriptCommittedRow["Visibility"],
): boolean {
  if (isKnownDeveloperContext(notice) || notice.Reason === "compaction") return false;
  if (notice.Diagnostic?.Code === "reviewer_suggestions") return false;
  if (notice.MessageType === "error_feedback") return true;
  if (isEmptyUnknownContext(notice, visibility)) return true;
  if (
    notice.MessageType === "runtime_diagnostic" ||
    (notice.Reason === "runtime_diagnostic" && notice.Severity !== "error")
  ) {
    return false;
  }
  return true;
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

function noticeIcon(notice: TranscriptNotice): LucideIcon {
  if (notice.Severity === "error") return CircleX;
  const typedIcon = noticeMessageTypeIcon(notice);
  if (typedIcon !== undefined) return typedIcon;
  const reasonIcon = noticeReasonIcon(notice);
  if (reasonIcon !== undefined) return reasonIcon;
  const diagnosticIcon = noticeDiagnosticIcon(notice);
  if (diagnosticIcon !== undefined) return diagnosticIcon;
  return notice.Severity === "warning" ? TriangleAlert : Info;
}

function noticeMessageTypeIcon(notice: TranscriptNotice): LucideIcon | undefined {
  switch (notice.MessageType) {
    case undefined:
    case null:
      return undefined;
    case "error_feedback":
      return CircleX;
    case "compaction_soon_reminder":
      return TriangleAlert;
    case "worktree_mode":
    case "worktree_mode_exit":
      return GitBranch;
    default:
      return undefined;
  }
}

function noticeReasonIcon(notice: TranscriptNotice): LucideIcon | undefined {
  switch (notice.Reason) {
    case "compaction":
      return RefreshCw;
    case "cache_warning":
      return Database;
    case "tool_output_repair":
      return Wrench;
    case "provider_model_mismatch":
      return Server;
    case "legacy_untyped_notice":
    case "runtime_diagnostic":
      return undefined;
  }
}

function noticeDiagnosticIcon(notice: TranscriptNotice): LucideIcon | undefined {
  switch (notice.Diagnostic?.Code) {
    case undefined:
      return undefined;
    case "reviewer_error":
      return CircleX;
    case "reviewer_suggestions":
      return Info;
    default:
      return undefined;
  }
}

function noticeIconTone(notice: TranscriptNotice): TranscriptFlatRowIconTone {
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

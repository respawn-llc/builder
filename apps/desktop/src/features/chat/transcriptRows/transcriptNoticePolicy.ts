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

export type TranscriptNoticeLabels = Readonly<{
  structuredNoticeCompactText(notice: TranscriptNotice): string;
}>;

export type TranscriptNoticeTextCopy = Readonly<{
  cacheWarning(scope: string, reason: string, lostInputTokens: number | null | undefined): string;
  compaction(count: number | null | undefined): string;
  toolOutputRepair(kind: "fresh_resource" | "live_provider_rejection", count: number): string;
  providerModelMismatch(servedModel: string, requestedModel: string): string;
  worktreeEnter(branch: string | null | undefined, worktreePath: string, effectiveCwd: string): string;
  worktreeExit(effectiveCwd: string): string;
  sessionRebind(): string;
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
  labels: TranscriptNoticeLabels,
  copy: TranscriptNoticeTextCopy,
): TranscriptNoticePolicy | null {
  const notice = row.Notice;
  if (notice === null || shouldOmitNotice(row, notice)) return null;

  const body = isMarkdownNotice(notice)
    ? ({ kind: "markdown", text: noticeOriginalText(notice) } as const)
    : ({ kind: "plain_text", text: structuredNoticeText(notice, copy) } as const);
  const copyText =
    body.kind === "markdown" && notice.Reason !== "compaction"
      ? body.text
      : structuredNoticeText(notice, copy);
  return {
    summary: noticeCompactText(notice, labels, copy),
    icon: noticeIcon(notice),
    iconTone: noticeIconTone(notice),
    defaultExpanded: noticeDefaultExpanded(notice, row.Visibility),
    body,
    copyText,
  };
}

export function structuredNoticeText(notice: TranscriptNotice, copy: TranscriptNoticeTextCopy): string {
  const reasonText = reasonNoticeText(notice, copy, true);
  if (reasonText !== undefined) return reasonText;
  const worktreeText = worktreeNoticeText(notice, copy, true);
  if (worktreeText !== undefined) return worktreeText;
  if (notice.MessageType === "session_rebind" && notice.Diagnostic?.Detail === undefined) {
    return copy.sessionRebind();
  }
  return firstPresent(
    notice.Diagnostic?.Detail,
    notice.LegacyText,
    notice.CondensedText,
    notice.CompactLabel,
    notice.SourcePath,
    notice.Reason,
  );
}

export function structuredNoticeCompactText(
  notice: TranscriptNotice,
  copy: TranscriptNoticeTextCopy,
): string {
  const reasonText = reasonNoticeText(notice, copy, false);
  if (reasonText !== undefined) return reasonText;
  const worktreeText = worktreeNoticeText(notice, copy, false);
  if (worktreeText !== undefined) return worktreeText;
  if (notice.MessageType === "session_rebind" && notice.Diagnostic?.Detail === undefined) {
    return copy.sessionRebind();
  }
  return firstPresent(
    notice.CondensedText,
    notice.LegacyText,
    notice.CompactLabel,
    notice.SourcePath,
    notice.Diagnostic?.Detail,
    notice.Reason,
  );
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

function noticeCompactText(
  notice: TranscriptNotice,
  labels: TranscriptNoticeLabels,
  copy: TranscriptNoticeTextCopy,
): string {
  const typedText = firstPresent(
    notice.CompactLabel,
    notice.CondensedText,
    notice.LegacyText,
    notice.SourcePath,
  );
  if (typedText !== "") return typedText;
  return firstPresent(
    structuredNoticeCompactText(notice, copy),
    labels.structuredNoticeCompactText(notice),
    notice.Reason,
  );
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

function reasonNoticeText(
  notice: TranscriptNotice,
  copy: TranscriptNoticeTextCopy,
  expanded: boolean,
): string | undefined {
  switch (notice.Reason) {
    case "cache_warning":
      return cacheWarningText(notice, copy);
    case "compaction":
      return compactionText(notice, copy, expanded);
    case "tool_output_repair":
      return toolOutputRepairText(notice, copy);
    case "provider_model_mismatch":
      return providerModelMismatchText(notice, copy);
    case "legacy_untyped_notice":
    case "runtime_diagnostic":
      return undefined;
  }
}

function cacheWarningText(notice: TranscriptNotice, copy: TranscriptNoticeTextCopy): string | undefined {
  const warning = notice.CacheWarning;
  return warning === undefined || warning === null
    ? undefined
    : copy.cacheWarning(warning.Scope, warning.Reason, warning.LostInputTokens);
}

function compactionText(
  notice: TranscriptNotice,
  copy: TranscriptNoticeTextCopy,
  expanded: boolean,
): string | undefined {
  const compaction = notice.Compaction;
  if (compaction === undefined || compaction === null) return undefined;
  if (
    expanded &&
    compaction.Detail !== undefined &&
    compaction.Detail !== null &&
    compaction.Detail.trim() !== ""
  ) {
    return compaction.Detail;
  }
  return copy.compaction(compaction.Count);
}

function toolOutputRepairText(notice: TranscriptNotice, copy: TranscriptNoticeTextCopy): string | undefined {
  const repair = notice.ToolOutputRepair;
  return repair === undefined || repair === null
    ? undefined
    : copy.toolOutputRepair(repair.kind, repair.count);
}

function providerModelMismatchText(
  notice: TranscriptNotice,
  copy: TranscriptNoticeTextCopy,
): string | undefined {
  const mismatch = notice.ProviderModelMismatch;
  return mismatch === undefined || mismatch === null
    ? undefined
    : copy.providerModelMismatch(mismatch.served_model, mismatch.requested_model);
}

function worktreeNoticeText(
  notice: TranscriptNotice,
  copy: TranscriptNoticeTextCopy,
  expanded: boolean,
): string | undefined {
  if (notice.Worktree === undefined || notice.Worktree === null) return undefined;
  if (expanded && notice.Diagnostic?.Detail !== undefined && notice.Diagnostic.Detail.trim() !== "") {
    return notice.Diagnostic.Detail;
  }
  if (notice.MessageType === "worktree_mode") {
    return copy.worktreeEnter(
      notice.Worktree.Branch,
      notice.Worktree.WorktreePath,
      notice.Worktree.EffectiveCwd,
    );
  }
  if (notice.MessageType === "worktree_mode_exit") {
    return copy.worktreeExit(notice.Worktree.EffectiveCwd);
  }
  return undefined;
}

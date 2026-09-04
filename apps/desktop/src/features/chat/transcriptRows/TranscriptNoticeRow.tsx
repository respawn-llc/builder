import { useTranslation } from "react-i18next";
import { StaticMarkdown } from "@/ui";

import type { ChatTranscriptCommittedRow } from "@/api";
import { basename } from "@/app-facade";
import { firstPresent } from "@/shared/text";

import { projectNotice, type TranscriptNotice, type TranscriptNoticeProse } from "./transcriptNoticePolicy";
import { TranscriptFlatRow } from "./TranscriptFlatRow";

export function TranscriptNoticeRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  const { t } = useTranslation();
  if (row.Kind !== "notice") return null;

  const notice = row.Notice;
  if (notice === null) return null;
  const policy = projectNotice(row, noticeProse(notice, t));
  if (policy === null) return null;
  const Icon = policy.icon;

  return (
    <TranscriptFlatRow
      body={
        policy.body.kind === "markdown" ? (
          <StaticMarkdown value={policy.body.text} />
        ) : (
          <p className="chat-transcript-row-body">{policy.body.text}</p>
        )
      }
      copyText={policy.copyText}
      defaultExpanded={policy.defaultExpanded}
      icon={<Icon className="size-4" />}
      iconTone={policy.iconTone}
      summary={policy.summary}
    />
  );
}

type Translate = ReturnType<typeof useTranslation>["t"];

function noticeProse(notice: TranscriptNotice, t: Translate): TranscriptNoticeProse {
  return {
    expanded: structuredNoticeText(notice, t, true),
    compact: structuredNoticeText(notice, t, false),
  };
}

function structuredNoticeText(notice: TranscriptNotice, t: Translate, expanded: boolean): string {
  const reasonText = reasonNoticeText(notice, t, expanded);
  if (reasonText !== undefined) return reasonText;
  const worktreeText = worktreeNoticeText(notice, t, expanded);
  if (worktreeText !== undefined) return worktreeText;
  if (notice.MessageType === "session_rebind" && notice.Diagnostic?.Detail === undefined) {
    return t("chatTranscript.notice.sessionRebind");
  }
  return expanded
    ? (firstPresent(
        notice.Diagnostic?.Detail,
        notice.LegacyText,
        notice.CondensedText,
        notice.CompactLabel,
        notice.SourcePath,
        notice.Reason,
      ) ?? notice.Reason)
    : (firstPresent(
        notice.CondensedText,
        notice.LegacyText,
        notice.CompactLabel,
        notice.SourcePath,
        notice.Diagnostic?.Detail,
        notice.Reason,
      ) ?? notice.Reason);
}

function reasonNoticeText(notice: TranscriptNotice, t: Translate, expanded: boolean): string | undefined {
  switch (notice.Reason) {
    case "cache_warning":
      return cacheWarningText(notice, t);
    case "compaction":
      return compactionText(notice, t, expanded);
    case "tool_output_repair":
      return toolOutputRepairText(notice, t);
    case "provider_model_mismatch":
      return providerModelMismatchText(notice, t);
    case "legacy_untyped_notice":
    case "runtime_diagnostic":
      return undefined;
  }
}

function cacheWarningText(notice: TranscriptNotice, t: Translate): string | undefined {
  const warning = notice.CacheWarning;
  if (warning === undefined || warning === null) return undefined;
  const reasonKey =
    warning.Reason === "compaction"
      ? "chatTranscript.notice.cacheReasonCompaction"
      : warning.Reason === "non_postfix"
        ? warning.Scope === "reviewer"
          ? "chatTranscript.notice.cacheReasonReviewerNonPostfix"
          : "chatTranscript.notice.cacheReasonNonPostfix"
        : warning.Reason === "reuse_dropped"
          ? warning.Scope === "reviewer"
            ? "chatTranscript.notice.cacheReasonReviewerReuseDropped"
            : "chatTranscript.notice.cacheReasonReuseDropped"
          : "chatTranscript.notice.cacheReasonUnknown";
  const reasonText = t(reasonKey, { reason: warning.Reason });
  if (
    warning.LostInputTokens === undefined ||
    warning.LostInputTokens === null ||
    warning.LostInputTokens <= 0
  ) {
    return t("chatTranscript.notice.cacheMiss", { reason: reasonText });
  }
  return t("chatTranscript.notice.cacheMissWithTokens", {
    reason: reasonText,
    tokens: formatTokenDeltaThousands(warning.LostInputTokens),
  });
}

function compactionText(notice: TranscriptNotice, t: Translate, expanded: boolean): string | undefined {
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
  return compaction.Count === undefined || compaction.Count === null
    ? t("chatTranscript.notice.compaction")
    : t("chatTranscript.notice.compactionCount", { ordinal: formatOrdinal(compaction.Count) });
}

function toolOutputRepairText(notice: TranscriptNotice, t: Translate): string | undefined {
  const repair = notice.ToolOutputRepair;
  if (repair === undefined || repair === null) return undefined;
  const noun = t(repair.count === 1 ? "chatTranscript.notice.toolCall" : "chatTranscript.notice.toolCalls");
  switch (repair.kind) {
    case "fresh_resource":
      return t("chatTranscript.notice.repairFreshResource", { count: repair.count, noun });
    case "live_provider_rejection":
      return t("chatTranscript.notice.repairLiveProviderRejection", { count: repair.count, noun });
    default:
      return assertUnreachable(repair.kind);
  }
}

function providerModelMismatchText(notice: TranscriptNotice, t: Translate): string | undefined {
  const mismatch = notice.ProviderModelMismatch;
  return mismatch === undefined || mismatch === null
    ? undefined
    : t("chatTranscript.notice.providerModelMismatch", {
        requested: mismatch.requested_model,
        served: mismatch.served_model,
      });
}

function worktreeNoticeText(notice: TranscriptNotice, t: Translate, expanded: boolean): string | undefined {
  const worktree = notice.Worktree;
  if (worktree === undefined || worktree === null) return undefined;
  if (expanded && notice.Diagnostic?.Detail !== undefined && notice.Diagnostic.Detail.trim() !== "") {
    return notice.Diagnostic.Detail;
  }
  if (notice.MessageType === "worktree_mode") {
    const name =
      firstPresent(worktree.Branch, basename(worktree.WorktreePath)) ?? t("chatTranscript.notice.worktree");
    return worktree.EffectiveCwd.trim().length === 0
      ? t("chatTranscript.notice.worktreeEnter", { name })
      : t("chatTranscript.notice.worktreeEnterCwd", { cwd: worktree.EffectiveCwd, name });
  }
  if (notice.MessageType === "worktree_mode_exit") {
    return worktree.EffectiveCwd.trim().length === 0
      ? t("chatTranscript.notice.worktreeExit")
      : t("chatTranscript.notice.worktreeExitCwd", { cwd: worktree.EffectiveCwd });
  }
  return undefined;
}

function assertUnreachable(value: never): never {
  void value;
  throw new Error("Unsupported tool output repair kind.");
}

function formatOrdinal(value: number): string {
  const remainder100 = value % 100;
  if (remainder100 >= 11 && remainder100 <= 13) return `${String(value)}th`;
  switch (value % 10) {
    case 1:
      return `${String(value)}st`;
    case 2:
      return `${String(value)}nd`;
    case 3:
      return `${String(value)}rd`;
    default:
      return `${String(value)}th`;
  }
}

function formatTokenDeltaThousands(tokens: number): string {
  if (tokens < 10_000) {
    const formatted = (tokens / 1_000).toFixed(1);
    return formatted.endsWith(".0") ? `${formatted.slice(0, -2)}k` : `${formatted}k`;
  }
  return `${String(Math.floor((tokens + 500) / 1_000))}k`;
}

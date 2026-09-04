import {
  firstPresent,
  type TranscriptNotice,
  type TranscriptRowContentFormatter,
} from "./transcriptRowProjector";

export type TranscriptNoticeTextCopy = Readonly<{
  cacheWarning(scope: string, reason: string, lostInputTokens: number | null | undefined): string;
  compaction(count: number | null | undefined): string;
  toolOutputRepair(kind: "fresh_resource" | "live_provider_rejection", count: number): string;
  providerModelMismatch(servedModel: string, requestedModel: string): string;
  worktreeEnter(branch: string | null | undefined, worktreePath: string, effectiveCwd: string): string;
  worktreeExit(effectiveCwd: string): string;
  sessionRebind(): string;
}>;

export function createTranscriptRowContentFormatter(
  copy: TranscriptNoticeTextCopy,
): TranscriptRowContentFormatter {
  return {
    structuredNoticeText: (notice) => structuredNoticeText(notice, copy),
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
  if (warning === undefined || warning === null) return undefined;
  return copy.cacheWarning(warning.Scope, warning.Reason, warning.LostInputTokens);
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

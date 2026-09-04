import { useTranslation } from "react-i18next";

import type { ChatTranscriptCommittedRow } from "@/api";

import { firstPresent, projectTranscriptRow } from "./transcriptRowProjector";
import { createTranscriptRowContentFormatter, structuredNoticeCompactText } from "./transcriptRowText";
import { TranscriptContentBody, TranscriptFlatRow } from "./TranscriptFlatRow";

export function TranscriptNoticeRow({ row }: Readonly<{ row: ChatTranscriptCommittedRow }>) {
  const { t } = useTranslation();
  if (row.Kind !== "notice") return null;

  const formatter = createTranscriptRowContentFormatter(noticeTextCopy(t));
  const projection = projectTranscriptRow(row, {
    reviewerFeedbackCompactText: (count) => t("chatTranscript.reviewerSuggestions", { count }),
    structuredNoticeCompactText: (notice) => structuredNoticeCompactText(notice, noticeTextCopy(t)),
  });
  if (projection === null) return null;

  return (
    <TranscriptFlatRow
      body={<TranscriptContentBody content={projection.body} formatter={formatter} />}
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

function noticeTextCopy(t: ReturnType<typeof useTranslation>["t"]) {
  return {
    cacheWarning(scope: string, reason: string, lostInputTokens: number | null | undefined) {
      const reasonKey =
        reason === "compaction"
          ? "chatTranscript.notice.cacheReasonCompaction"
          : reason === "non_postfix"
            ? scope === "reviewer"
              ? "chatTranscript.notice.cacheReasonReviewerNonPostfix"
              : "chatTranscript.notice.cacheReasonNonPostfix"
            : reason === "reuse_dropped"
              ? scope === "reviewer"
                ? "chatTranscript.notice.cacheReasonReviewerReuseDropped"
                : "chatTranscript.notice.cacheReasonReuseDropped"
              : "chatTranscript.notice.cacheReasonUnknown";
      const reasonText = t(reasonKey, { reason });
      if (lostInputTokens === undefined || lostInputTokens === null || lostInputTokens <= 0) {
        return t("chatTranscript.notice.cacheMiss", { reason: reasonText });
      }
      return t("chatTranscript.notice.cacheMissWithTokens", {
        reason: reasonText,
        tokens: formatTokenDeltaThousands(lostInputTokens),
      });
    },
    compaction(count: number | null | undefined) {
      return count === undefined || count === null
        ? t("chatTranscript.notice.compaction")
        : t("chatTranscript.notice.compactionCount", { ordinal: formatOrdinal(count) });
    },
    toolOutputRepair(kind: "fresh_resource" | "live_provider_rejection", count: number) {
      const noun = t(count === 1 ? "chatTranscript.notice.toolCall" : "chatTranscript.notice.toolCalls");
      switch (kind) {
        case "fresh_resource":
          return t("chatTranscript.notice.repairFreshResource", { count, noun });
        case "live_provider_rejection":
          return t("chatTranscript.notice.repairLiveProviderRejection", { count, noun });
        default:
          return assertUnreachable(kind);
      }
    },
    providerModelMismatch(servedModel: string, requestedModel: string) {
      return t("chatTranscript.notice.providerModelMismatch", {
        requested: requestedModel,
        served: servedModel,
      });
    },
    worktreeEnter(branch: string | null | undefined, worktreePath: string, effectiveCwd: string) {
      const name = firstPresent(branch, pathBasename(worktreePath)) || t("chatTranscript.notice.worktree");
      return effectiveCwd.trim().length === 0
        ? t("chatTranscript.notice.worktreeEnter", { name })
        : t("chatTranscript.notice.worktreeEnterCwd", { cwd: effectiveCwd, name });
    },
    worktreeExit(effectiveCwd: string) {
      return effectiveCwd.trim().length === 0
        ? t("chatTranscript.notice.worktreeExit")
        : t("chatTranscript.notice.worktreeExitCwd", { cwd: effectiveCwd });
    },
    sessionRebind() {
      return t("chatTranscript.notice.sessionRebind");
    },
  };
}

function assertUnreachable(value: never): never {
  void value;
  throw new Error("Unsupported tool output repair kind.");
}

function pathBasename(path: string): string {
  const trimmed = path.trim();
  const slash = trimmed.lastIndexOf("/");
  const backslash = trimmed.lastIndexOf("\\");
  const separator = Math.max(slash, backslash);
  const basename = separator < 0 ? trimmed : trimmed.slice(separator + 1);
  return basename === "" || basename === "." ? "" : basename;
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

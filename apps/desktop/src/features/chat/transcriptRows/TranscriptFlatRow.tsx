import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { TranscriptDisclosure } from "@/ui";

import { TranscriptCopyAction } from "./TranscriptCopyAction";

export type TranscriptFlatRowIconTone = "neutral" | "warning" | "error" | "success";

export function TranscriptFlatRow({
  body,
  copyText,
  defaultExpanded,
  icon,
  iconTone,
  summary,
}: Readonly<{
  body: ReactNode;
  copyText: string;
  defaultExpanded: boolean;
  icon: ReactNode;
  iconTone: TranscriptFlatRowIconTone;
  summary: string;
}>) {
  const { t } = useTranslation();
  return (
    <TranscriptDisclosure
      actions={
        <TranscriptCopyAction
          copiedLabel={t("chatTranscript.copied")}
          copyLabel={t("chatTranscript.copy")}
          failureLabel={t("chatTranscript.copyFailed")}
          value={copyText}
        />
      }
      body={body}
      collapseLabel={t("app.collapse")}
      defaultExpanded={defaultExpanded}
      expandLabel={t("app.expand")}
      icon={icon}
      iconTone={iconTone}
      summary={summary}
    />
  );
}

import type { ReactNode } from "react";

import { TranscriptDisclosure } from "@/ui";

import { TranscriptCopyAction } from "./TranscriptCopyAction";

export type TranscriptFlatRowIconTone = "neutral" | "warning" | "error" | "success";

export type TranscriptFlatRowLabels = Readonly<{
  collapseLabel: string;
  expandLabel: string;
  copyLabel: string;
  copiedLabel: string;
  copyFailedLabel: string;
}>;

export function TranscriptFlatRow({
  body,
  copyText,
  defaultExpanded,
  icon,
  iconTone,
  labels,
  summary,
}: Readonly<{
  body: ReactNode;
  copyText: string;
  defaultExpanded: boolean;
  icon: ReactNode;
  iconTone: TranscriptFlatRowIconTone;
  labels: TranscriptFlatRowLabels;
  summary: string;
}>) {
  return (
    <TranscriptDisclosure
      actions={
        <TranscriptCopyAction
          copiedLabel={labels.copiedLabel}
          copyLabel={labels.copyLabel}
          failureLabel={labels.copyFailedLabel}
          value={copyText}
        />
      }
      body={body}
      collapseLabel={labels.collapseLabel}
      defaultExpanded={defaultExpanded}
      expandLabel={labels.expandLabel}
      icon={icon}
      iconTone={iconTone}
      summary={summary}
    />
  );
}

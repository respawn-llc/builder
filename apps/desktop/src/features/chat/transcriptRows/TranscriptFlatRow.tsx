import {
  CircleAlert,
  CircleX,
  Database,
  GitBranch,
  Info,
  MessageCircle,
  RefreshCw,
  Server,
  TriangleAlert,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";

import { StaticMarkdown, TranscriptDisclosure } from "@/ui";

import {
  transcriptRowContentText,
  type TranscriptRowContent,
  type TranscriptRowContentFormatter,
  type TranscriptRowIcon,
  type TranscriptRowProjection,
} from "./transcriptRowProjector";
import { TranscriptCopyAction } from "./TranscriptCopyAction";

export type TranscriptFlatRowLabels = Readonly<{
  collapseLabel: string;
  expandLabel: string;
  copyLabel: string;
  copiedLabel: string;
  copyFailedLabel: string;
}>;

export function TranscriptFlatRow({
  body,
  formatter,
  labels,
  projection,
}: Readonly<{
  body: ReactNode;
  formatter: TranscriptRowContentFormatter;
  labels: TranscriptFlatRowLabels;
  projection: TranscriptRowProjection;
}>) {
  return (
    <TranscriptDisclosure
      actions={
        <TranscriptCopyAction
          copiedLabel={labels.copiedLabel}
          copyLabel={labels.copyLabel}
          failureLabel={labels.copyFailedLabel}
          value={transcriptRowContentText(projection.copySource, formatter)}
        />
      }
      body={body}
      collapseLabel={labels.collapseLabel}
      defaultExpanded={projection.defaultExpanded}
      expandLabel={labels.expandLabel}
      icon={<TranscriptRowIcon icon={projection.icon} />}
      iconTone={projection.iconTone}
      summary={projection.compactText}
    />
  );
}

export function TranscriptContentBody({
  content,
  formatter,
}: Readonly<{
  content: TranscriptRowContent;
  formatter: TranscriptRowContentFormatter;
}>) {
  switch (content.kind) {
    case "markdown":
      return <StaticMarkdown value={content.text} />;
    case "plain_text":
    case "reviewer_error":
      return <p className="chat-transcript-row-body">{content.text}</p>;
    case "structured_notice":
      return <p className="chat-transcript-row-body">{transcriptRowContentText(content, formatter)}</p>;
    case "reviewer_feedback":
    case "ask_question":
      throw new Error(`Content kind ${content.kind} requires a specialized row body.`);
  }
}

function TranscriptRowIcon({ icon }: Readonly<{ icon: TranscriptRowIcon }>) {
  const Icon = transcriptRowIconComponents[icon];
  return <Icon className="size-4" />;
}

const transcriptRowIconComponents: Record<TranscriptRowIcon, LucideIcon> = {
  notice: Info,
  notice_error: CircleX,
  notice_warning: TriangleAlert,
  notice_compaction: RefreshCw,
  notice_worktree: GitBranch,
  notice_cache: Database,
  notice_repair: Wrench,
  notice_provider: Server,
  reviewer_feedback: MessageCircle,
  reviewer_error: CircleX,
  ask_question: CircleAlert,
  ask_question_error: CircleX,
};

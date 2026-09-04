import { Copy, FileDiff, Globe, Terminal, Wrench } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import {
  cx,
  IconTooltipButton,
  showStatusToast,
  Spinner,
  SyntaxHighlightedCode,
  TranscriptDisclosure,
  type TranscriptDisclosureIconTone,
} from "@/ui";
import {
  resolveToolPresentation,
  type PatchChangedFile,
  type ToolPresentation,
  type ToolTextSection,
} from "./toolPresentation";
import type { TranscriptToolSlotItem } from "./toolSlotTypes";
import "./TranscriptToolSlot.css";

export function TranscriptToolSlot({ item }: Readonly<{ item: TranscriptToolSlotItem }>) {
  const { t } = useTranslation();
  const presentation = resolveToolPresentation(item, {
    backgrounded: t("chat.toolRows.backgrounded"),
    editFailed: t("chat.toolRows.editFailed"),
    exitCode: (code) => t("chat.toolRows.exitCode", { code }),
    moreLines: (count) => t("chat.toolRows.moreLines", { count }),
    patchFailed: t("chat.toolRows.patchFailed"),
    searchedWeb: (query) => t("chat.toolRows.searchedWeb", { query }),
    viewedImage: (path) => t("chat.toolRows.viewedImage", { path }),
  });
  switch (presentation.kind) {
    case "generic":
    case "shell-input":
    case "view-image":
    case "shell-command":
      return <ExpandableToolRow presentation={presentation} />;
    case "patch-changes":
      return <PatchChangesRow presentation={presentation} />;
    case "patch-invalid-input":
      return <PatchInvalidInputRow presentation={presentation} />;
    case "web-search":
      return (
        <StaticToolRow
          compact={presentation.compact}
          icon={
            presentation.running ? (
              <Spinner size="sm" />
            ) : (
              <Globe aria-hidden="true" className="size-4" strokeWidth={1.8} />
            )
          }
          iconTone={presentation.iconTone}
        />
      );
  }
}

type ExpandableToolPresentation = Exclude<
  ToolPresentation,
  | Readonly<{ kind: "patch-changes" }>
  | Readonly<{ kind: "patch-invalid-input" }>
  | Readonly<{ kind: "web-search" }>
>;

function PatchInvalidInputRow({
  presentation,
}: Readonly<{
  presentation: Extract<ToolPresentation, Readonly<{ kind: "patch-invalid-input" }>>;
}>) {
  const { t } = useTranslation();
  const icon = presentation.running ? (
    <Spinner size="sm" />
  ) : (
    <FileDiff aria-hidden="true" className="size-4" strokeWidth={1.8} />
  );
  if (presentation.detail === undefined) {
    return <StaticToolRow compact={presentation.compact} icon={icon} iconTone={presentation.iconTone} />;
  }
  return (
    <TranscriptDisclosure
      body={<pre className="transcript-tool-plain-text">{presentation.detail}</pre>}
      collapseLabel={t("chat.toolRows.collapse")}
      defaultExpanded={false}
      expandLabel={t("chat.toolRows.expand")}
      icon={icon}
      iconTone={presentation.iconTone}
      summary={presentation.compact}
    />
  );
}

function StaticToolRow({
  compact,
  icon,
  iconTone,
}: Readonly<{
  compact: string;
  icon: ReactNode;
  iconTone: TranscriptDisclosureIconTone;
}>) {
  return (
    <div className="transcript-tool-static-row min-h-9">
      <span
        aria-hidden="true"
        className={cx("transcript-tool-static-icon size-5", `transcript-disclosure-icon--${iconTone}`)}
      >
        {icon}
      </span>
      <span className="min-w-0 truncate text-sm text-[var(--color-on-background)]">{compact}</span>
    </div>
  );
}

function PatchChangesRow({
  presentation,
}: Readonly<{ presentation: Extract<ToolPresentation, Readonly<{ kind: "patch-changes" }>> }>) {
  const { t } = useTranslation();
  return (
    <TranscriptDisclosure
      body={<PatchChangesDetails diagnostic={presentation.diagnostic} files={presentation.files} />}
      collapseLabel={t("chat.toolRows.collapse")}
      defaultExpanded={false}
      expandLabel={t("chat.toolRows.expand")}
      icon={
        presentation.running ? (
          <Spinner size="sm" />
        ) : (
          <FileDiff aria-hidden="true" className="size-4" strokeWidth={1.8} />
        )
      }
      iconTone={presentation.iconTone}
      summary={<PatchChangesSummary files={presentation.files} label={t("chat.toolRows.edited")} />}
      summaryMode="multiline"
    />
  );
}

function PatchChangesSummary({
  files,
  label,
}: Readonly<{ files: readonly PatchChangedFile[]; label: string }>) {
  return (
    <span className="transcript-patch-summary">
      <span className="transcript-patch-summary-label">{label}</span>
      {files.map((file) => (
        <span className="transcript-patch-summary-file" key={file.Path.Absolute}>
          <span className="transcript-patch-path">{file.Path.Relative}</span>
          <PatchCounts file={file} />
        </span>
      ))}
    </span>
  );
}

function PatchCounts({ file }: Readonly<{ file: PatchChangedFile }>) {
  const wholeFileDeletion = hasOnlyWholeFileDeletion(file);
  return (
    <span className="transcript-patch-counts">
      {file.Removed !== null && (file.Removed > 0 || wholeFileDeletion) ? (
        <span className="transcript-patch-count transcript-patch-count--removed">-{file.Removed}</span>
      ) : null}
      {file.Added > 0 ? (
        <span className="transcript-patch-count transcript-patch-count--added">+{file.Added}</span>
      ) : null}
    </span>
  );
}

function PatchChangesDetails({
  diagnostic,
  files,
}: Readonly<{ diagnostic?: string | undefined; files: readonly PatchChangedFile[] }>) {
  return (
    <div className="transcript-patch-details">
      <div className="transcript-patch-files">
        {files.map((file) => (
          <section className="transcript-patch-file" key={file.Path.Absolute}>
            <div className="transcript-patch-detail-path">
              <span className="transcript-patch-path">{file.Path.Absolute}</span>
              {hasOnlyWholeFileDeletion(file) ? <PatchCounts file={file} /> : null}
            </div>
            {file.Operations.flatMap((operation) => operation.Groups).map((group, groupIndex) => (
              <SyntaxHighlightedCode
                code={group.Lines.map((line) => line.Content).join("\n")}
                key={groupIndex}
                languageHint={file.Path.Relative}
                lineClassName={(lineIndex) =>
                  cx(
                    "transcript-patch-source-line",
                    group.Lines[lineIndex]?.Kind === "added"
                      ? "transcript-patch-source-line--added"
                      : "transcript-patch-source-line--removed",
                  )
                }
              />
            ))}
          </section>
        ))}
      </div>
      {diagnostic === undefined ? null : (
        <pre className="transcript-tool-plain-text transcript-patch-diagnostic">{diagnostic}</pre>
      )}
    </div>
  );
}

function hasOnlyWholeFileDeletion(file: PatchChangedFile): boolean {
  return file.Operations.every((operation) => operation.Kind === "delete");
}

function ExpandableToolRow({ presentation }: Readonly<{ presentation: ExpandableToolPresentation }>) {
  const { t } = useTranslation();
  return (
    <TranscriptDisclosure
      actions={
        presentation.copyPayload === undefined ? undefined : (
          <ToolCopyAction payload={presentation.copyPayload} />
        )
      }
      body={
        presentation.kind === "shell-command" ? (
          <ShellCommandDetails
            command={presentation.command}
            commandLanguage={presentation.commandLanguage}
            exitCode={presentation.exitCode}
            output={presentation.output}
            outputLanguage={presentation.outputLanguage}
          />
        ) : (
          <ToolTextSections sections={presentation.body} />
        )
      }
      collapseLabel={t("chat.toolRows.collapse")}
      defaultExpanded={false}
      expandLabel={t("chat.toolRows.expand")}
      icon={
        presentation.running ? (
          <Spinner size="sm" />
        ) : presentation.icon === "terminal" ? (
          <Terminal aria-hidden="true" className="size-4" strokeWidth={1.8} />
        ) : (
          <Wrench aria-hidden="true" className="size-4" strokeWidth={1.8} />
        )
      }
      iconTone={presentation.iconTone}
      liveStatus={
        presentation.status === undefined ? undefined : (
          <span className="transcript-tool-status text-xs">{presentation.status}</span>
        )
      }
      summary={
        presentation.kind === "shell-command" ? (
          <span className="transcript-tool-command-summary">{presentation.compact}</span>
        ) : (
          presentation.compact
        )
      }
    />
  );
}

function ShellCommandDetails({
  command,
  commandLanguage,
  exitCode,
  output,
  outputLanguage,
}: Readonly<{
  command: string;
  commandLanguage?: string | undefined;
  exitCode?: number | undefined;
  output?: string | undefined;
  outputLanguage?: string | undefined;
}>) {
  const { t } = useTranslation();
  return (
    <div className="transcript-tool-sections">
      <SyntaxHighlightedCode code={command} languageHint={commandLanguage} />
      {output === undefined ? null : outputLanguage === undefined ? (
        <pre className="transcript-tool-plain-text">{output}</pre>
      ) : (
        <SyntaxHighlightedCode code={output} languageHint={outputLanguage} />
      )}
      {exitCode === undefined ? null : (
        <div className="transcript-tool-exit-code text-xs">
          {t("chat.toolRows.exitCode", { code: exitCode })}
        </div>
      )}
    </div>
  );
}

function ToolTextSections({ sections }: Readonly<{ sections: readonly ToolTextSection[] }>) {
  return (
    <div className="transcript-tool-sections">
      {sections.map((section) =>
        section.kind === "source" ? (
          <SyntaxHighlightedCode
            code={section.content}
            key={section.id}
            languageHint={section.languageHint}
          />
        ) : (
          <pre className="transcript-tool-plain-text" key={section.id}>
            {section.content}
          </pre>
        ),
      )}
    </div>
  );
}

function ToolCopyAction({ payload }: Readonly<{ payload: string }>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  return (
    <IconTooltipButton
      label={t("chat.toolRows.copy")}
      onClick={() => {
        void writeClipboardText(payload, nativeBridge)
          .then(() => {
            showStatusToast({
              id: "chat-tool-row-copy-succeeded",
              title: t("chat.toolRows.copySucceeded"),
              tone: "success",
            });
          })
          .catch((cause: unknown) => {
            showStatusToast({
              body: errorMessage(cause),
              id: "chat-tool-row-copy-failed",
              title: t("chat.toolRows.copyFailed"),
              tone: "danger",
            });
          });
      }}
      size="icon-sm"
    >
      <Copy aria-hidden="true" className="size-4" strokeWidth={1.8} />
    </IconTooltipButton>
  );
}

import { useEffect, useId, useRef, useState, type RefObject } from "react";
import { ChevronDown, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TaskDetail } from "@/api";
import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { useOpenExternalLink } from "@/app-facade";
import { resumeTaskInitiatingAction, type TaskInitiatingActionController } from "@/shared/execution-target";
import { writeClipboardText } from "@/shared/native-clipboard";
import {
  Button,
  Island,
  MarkdownText,
  compactExternalUrlLabel,
  safeExternalUrl,
  showStatusToast,
} from "@/ui";
import { cx, fieldIslandInputClassName, useOpacityExit } from "@/ui";
import type { DescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { taskStatusTone } from "./taskStatusTone";
import { TaskExecutionTargetFacts } from "./TaskExecutionTargetFacts";
import { TaskDetailLabels } from "./TaskDetailLabels";
import { TaskPropertyLine } from "./TaskPropertyLine";
import { taskExecutionRoot } from "./taskExecutionTarget";
import type { useTaskMutations } from "./useTaskDetailData";

export type TaskDraft = Readonly<{
  title: string;
  body: string;
}>;

export function TaskHeaderIsland({
  detail,
  disabled,
  draft,
  onDraftChange,
  onSave,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  onDraftChange: (draft: TaskDraft) => void;
  onSave: (draft?: TaskDraft) => Promise<void>;
}>) {
  const { t } = useTranslation();
  const title = draft.title;
  const dirty = draft.title !== detail.title || draft.body !== detail.body;

  function nextTitle(value: string): TaskDraft {
    return { ...draft, title: value.replaceAll("\n", " ") };
  }

  return (
    <form
      className="flex min-w-0 items-center gap-[var(--space-2)]"
      data-testid="task-detail-title-island"
      onSubmit={(event) => {
        event.preventDefault();
        void onSave();
      }}
    >
      <input
        aria-label={t("task.name")}
        className={cx(
          fieldIslandInputClassName(1),
          "min-w-0 flex-1 px-[var(--space-3)] py-[var(--space-2)] text-[1.125rem] font-bold",
        )}
        disabled={disabled}
        onChange={(event) => {
          onDraftChange(nextTitle(event.target.value));
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            event.currentTarget.form?.requestSubmit();
          }
        }}
        type="text"
        value={title}
      />
      {dirty ? (
        <Button
          aria-label={t("task.save")}
          className="shrink-0"
          data-testid="task-detail-save"
          disabled={disabled || title.trim().length === 0}
          size="icon"
          type="submit"
          variant="primary"
        >
          <Save aria-hidden="true" size={16} strokeWidth={1.75} />
        </Button>
      ) : null}
    </form>
  );
}

export function DescriptionIsland({
  disabled,
  draft,
  error,
  onDraftChange,
  onPresentationChange,
  presentation,
}: Readonly<{
  disabled: boolean;
  draft: TaskDraft;
  error: unknown;
  onDraftChange: (draft: TaskDraft) => void;
  onPresentationChange: (presentation: DescriptionPresentationState) => void;
  presentation: DescriptionPresentationState;
}>) {
  const descriptionId = useId();
  const descriptionErrorId = `${descriptionId}-error`;
  const descriptionError = error == null ? "" : errorMessage(error);

  return (
    <div className="grid min-h-0 min-w-0 gap-[var(--space-2)]" data-testid="task-description-input-frame">
      {presentation.editing && !disabled ? (
        <DescriptionEditor
          describedBy={descriptionError.length > 0 ? descriptionErrorId : undefined}
          error={descriptionError.length > 0}
          id={descriptionId}
          onBlur={() => {
            onPresentationChange({ ...presentation, editing: false });
          }}
          onChange={(body) => {
            onDraftChange({ ...draft, body });
          }}
          value={draft.body}
        />
      ) : (
        <DescriptionReadView
          disabled={disabled}
          expanded={presentation.expanded}
          onEdit={() => {
            onPresentationChange({ editing: true, expanded: true });
          }}
          onExpand={() => {
            onPresentationChange({ ...presentation, expanded: true });
          }}
          value={draft.body}
        />
      )}
      {descriptionError.length > 0 ? (
        <span className="text-[var(--color-error)]" id={descriptionErrorId}>
          {descriptionError}
        </span>
      ) : null}
    </div>
  );
}

function DescriptionEditor({
  describedBy,
  error,
  id,
  onBlur,
  onChange,
  value,
}: Readonly<{
  describedBy: string | undefined;
  error: boolean;
  id: string;
  onBlur: () => void;
  onChange: (value: string) => void;
  value: string;
}>) {
  const { t } = useTranslation();
  return (
    <textarea
      aria-describedby={describedBy}
      aria-invalid={error ? true : undefined}
      aria-label={t("task.description")}
      autoFocus
      className={cx(
        fieldIslandInputClassName(1),
        "block min-h-[220px] min-w-0 resize-none p-[var(--space-2)] font-mono",
      )}
      id={id}
      onBlur={onBlur}
      onChange={(event) => {
        onChange(event.target.value);
      }}
      placeholder={t("task.bodyPlaceholder")}
      value={value}
    />
  );
}

function DescriptionReadView({
  disabled,
  expanded,
  onEdit,
  onExpand,
  value,
}: Readonly<{
  disabled: boolean;
  expanded: boolean;
  onEdit: () => void;
  onExpand: () => void;
  value: string;
}>) {
  const { t } = useTranslation();
  const openExternalLink = useOpenExternalLink();
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const overflows = useDescriptionOverflow({ contentRef, enabled: !expanded, viewportRef });
  const affordancePhase = useOpacityExit(!expanded && overflows);
  return (
    <div className="relative min-w-0">
      <div
        aria-label={t("task.description")}
        aria-readonly
        className={cx(
          fieldIslandInputClassName(1),
          "block min-w-0 p-[var(--space-2)]",
          !disabled && "cursor-text",
          expanded ? "overflow-visible" : "max-h-[clamp(5lh,50dvh,10lh)] overflow-hidden",
        )}
        onFocus={(event) => {
          if (!disabled && event.target === event.currentTarget) {
            onEdit();
          }
        }}
        ref={viewportRef}
        role="textbox"
        tabIndex={disabled ? -1 : 0}
      >
        <div ref={contentRef}>
          {value.trim().length > 0 ? (
            <MarkdownText onOpenLink={openExternalLink} value={value} />
          ) : (
            <span className="text-[var(--color-muted)]">{t("task.bodyPlaceholder")}</span>
          )}
        </div>
      </div>
      {affordancePhase !== "hidden" ? (
        <>
          <div
            aria-hidden="true"
            className={cx(
              "pointer-events-none absolute inset-x-[var(--space-2)] bottom-[var(--space-2)] h-12 bg-gradient-to-b from-transparent to-[var(--color-island-1)] transition-opacity motion-reduce:transition-none",
              affordancePhase === "visible" ? "opacity-100" : "opacity-0",
            )}
            data-state={affordancePhase}
            data-testid="task-description-fade"
          />
          <button
            aria-label={t("app.expand")}
            className={cx(
              "app-region-no-drag absolute inset-x-0 bottom-0 grid h-10 place-items-center text-[var(--color-on-island)] transition-opacity motion-reduce:transition-none",
              affordancePhase === "visible"
                ? "pointer-events-auto opacity-100"
                : "pointer-events-none opacity-0",
            )}
            data-state={affordancePhase}
            data-testid="task-description-expand"
            onClick={onExpand}
            type="button"
          >
            <ChevronDown aria-hidden="true" size={20} strokeWidth={1.5} />
          </button>
        </>
      ) : null}
    </div>
  );
}

function useDescriptionOverflow({
  contentRef,
  enabled,
  viewportRef,
}: Readonly<{
  contentRef: RefObject<HTMLDivElement | null>;
  enabled: boolean;
  viewportRef: RefObject<HTMLDivElement | null>;
}>): boolean {
  const [overflows, setOverflows] = useState(false);
  useEffect(() => {
    if (!enabled) {
      return;
    }
    const measureOverflow = () => {
      const viewport = viewportRef.current;
      if (viewport !== null) {
        setOverflows(viewport.scrollHeight > viewport.clientHeight);
      }
    };
    const frame = window.requestAnimationFrame(measureOverflow);
    window.addEventListener("resize", measureOverflow);
    if (typeof ResizeObserver === "undefined") {
      return () => {
        window.cancelAnimationFrame(frame);
        window.removeEventListener("resize", measureOverflow);
      };
    }
    const observer = new ResizeObserver(measureOverflow);
    if (viewportRef.current !== null) {
      observer.observe(viewportRef.current);
    }
    if (contentRef.current !== null) {
      observer.observe(contentRef.current);
    }
    return () => {
      observer.disconnect();
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", measureOverflow);
    };
  }, [contentRef, enabled, viewportRef]);
  return enabled && overflows;
}

export function PropertiesIsland({
  detail,
  disabled,
  mutations,
  resumeContinuation,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  resumeContinuation: TaskInitiatingActionController;
}>) {
  const { t } = useTranslation();
  const openExternalLink = useOpenExternalLink();
  return (
    <Island
      aria-label={t("task.properties")}
      className="grid min-w-0 content-start gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius="l"
      unpadded
    >
      <dl className="m-0 grid min-w-0 gap-[var(--space-2)]">
        <TaskPropertyLine
          label={t("task.identifier", { defaultValue: "ID" })}
          value={<span className="font-mono">{detail.shortID}</span>}
        />
        <TaskDetailLabels disabled={disabled} />
        <TaskPropertyLine label={t("task.project")} value={detail.projectName} />
        <TaskPropertyLine
          label={t("task.status")}
          value={
            <TaskStatusText
              label={t(`task.statusKinds.${detail.status.kind}`)}
              tone={taskStatusTone(detail.status)}
            />
          }
        />
        <TaskExecutionTargetFacts detail={detail} />
        <TaskPropertyLine label={t("task.workflow")} value={detail.workflowName} />
        <SourceLine label={t("task.source")} onOpen={openExternalLink} value={detail.sourceURL} />
        <TaskPropertyLine label={t("task.sessions")} value={detail.retainedSessionCount.toString()} />
      </dl>
      <TaskActionPanel
        detail={detail}
        disabled={disabled}
        mutations={mutations}
        resumeContinuation={resumeContinuation}
      />
    </Island>
  );
}

function TaskActionPanel({
  detail,
  disabled,
  mutations,
  resumeContinuation,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  resumeContinuation: TaskInitiatingActionController;
}>) {
  const { t } = useTranslation();
  return (
    <>
      <div className="grid gap-[var(--space-2)] pt-[var(--space-1)]">
        <TaskOpenButtons detail={detail} disabled={disabled} />
        {detail.actions.canResume ? (
          <Button
            data-testid="task-detail-resume"
            disabled={disabled || resumeContinuation.running}
            onClick={() => {
              void resumeContinuation.run(resumeTaskInitiatingAction(detail.id));
            }}
            variant="primary"
          >
            {t("board.resume")}
          </Button>
        ) : null}
        {detail.actions.canInterrupt ? (
          <Button
            disabled={disabled || mutations.interrupt.isPending}
            onClick={() => {
              mutations.interrupt.mutate(undefined);
            }}
            variant="secondary"
          >
            {t("board.interrupt")}
          </Button>
        ) : null}
      </div>
    </>
  );
}

function TaskOpenButtons({ detail, disabled }: Readonly<{ detail: TaskDetail; disabled: boolean }>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const [openError, setOpenError] = useState("");
  const executionRoot = taskExecutionRoot(detail);
  const canOpenScript = nativeBridge.capabilities.files.open;

  async function openInCli(sessionID: string): Promise<void> {
    await writeClipboardText(`kent --session=${sessionID}`, nativeBridge);
    showStatusToast({
      id: "task-cli-command-copied",
      title: t("task.cliCommandCopied"),
      tone: "success",
    });
  }

  async function openScript(path: string): Promise<void> {
    await nativeBridge.files.openFile({ basePath: executionRoot, path });
  }

  return (
    <>
      {detail.liveSessionIDs.map((sessionID) => (
        <Button
          disabled={disabled}
          key={sessionID}
          onClick={() => {
            setOpenError("");
            void openInCli(sessionID).catch((cause: unknown) => {
              setOpenError(errorMessage(cause));
            });
          }}
          variant="secondary"
        >
          {t("task.openInCli")} <span className="truncate font-mono">{sessionID}</span>
        </Button>
      ))}
      {canOpenScript
        ? detail.currentScripts.map((script) => (
            <Button
              disabled={disabled}
              key={`${script.currentNode.nodeID}:${script.currentNode.transitionBranchKey ?? "serial"}`}
              onClick={() => {
                setOpenError("");
                void openScript(script.path).catch((cause: unknown) => {
                  setOpenError(errorMessage(cause));
                });
              }}
              variant="secondary"
            >
              {t("task.openScript")} <span className="truncate font-mono">{script.path}</span>
            </Button>
          ))
        : null}
      {openError.length > 0 ? <p className="m-0 text-sm text-[var(--color-error)]">{openError}</p> : null}
    </>
  );
}

function TaskStatusText({
  label,
  tone,
}: Readonly<{ label: string; tone: ReturnType<typeof taskStatusTone> }>) {
  return <span className={cx("font-bold", taskStatusTextClassName(tone))}>{label}</span>;
}

function taskStatusTextClassName(tone: ReturnType<typeof taskStatusTone>): string {
  if (tone === "info") {
    return "text-[var(--color-primary)]";
  }
  if (tone === "success") {
    return "text-[var(--color-success)]";
  }
  if (tone === "warning") {
    return "text-[var(--color-warning)]";
  }
  if (tone === "danger") {
    return "text-[var(--color-error)]";
  }
  return "text-[var(--color-muted)]";
}

function SourceLine({
  label,
  onOpen,
  value,
}: Readonly<{ label: string; onOpen: (url: string) => void; value: string }>) {
  const trimmed = value.trim();
  if (trimmed.length === 0) {
    return null;
  }
  const href = safeExternalUrl(trimmed);
  if (href === undefined) {
    return <TaskPropertyLine label={label} value={trimmed} />;
  }
  return (
    <TaskPropertyLine
      label={label}
      value={
        <a
          className="truncate text-[var(--color-primary)] underline-offset-2 hover:underline"
          data-testid="task-source-link"
          href={href}
          onClick={(event) => {
            event.preventDefault();
            onOpen(href);
          }}
          rel="noreferrer"
          target="_blank"
          title={href}
        >
          {compactExternalUrlLabel(href)}
        </a>
      }
    />
  );
}

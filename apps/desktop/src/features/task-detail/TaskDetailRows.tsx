import { useState } from "react";
import { Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TaskCurrentNode, TaskDetail } from "@/api";
import { errorMessage } from "@/api";
import { useAppServices, useTextFieldSubmitShortcut, useTextFieldSubmitShortcutPolicy } from "@/app-facade";
import { useOpenExternalLink } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import { taskStatusTone } from "@/shared/task-status";
import {
  Button,
  CollapsibleMarkdownField,
  Island,
  compactExternalUrlLabel,
  safeExternalUrl,
  showStatusToast,
} from "@/ui";
import { cx, fieldIslandInputClassName } from "@/ui";
import type { DescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { ellipsizeActionTarget } from "./ellipsizeActionTarget";
import { TaskExecutionTargetFacts } from "./TaskExecutionTargetFacts";
import { TaskDetailLabels } from "./TaskDetailLabels";
import { TaskDeleteButton } from "./TaskDeleteButton";
import { TaskResumeButton, TaskStartButton } from "./TaskResumeButton";
import { TaskPropertyLine } from "./TaskPropertyLine";
import { taskDetailIslandRadius } from "./taskDetailIslandStyles";
import { taskExecutionRoot } from "./taskExecutionTarget";
import type { useTaskMutations } from "./useTaskDetailData";

export type TaskDraft = Readonly<{
  title: string;
  body: string;
}>;

export function TaskHeaderIsland({
  canSaveDraft,
  detail,
  disabled,
  draft,
  onDraftChange,
  onSave,
}: Readonly<{
  canSaveDraft: boolean;
  detail: TaskDetail;
  disabled: boolean;
  draft: TaskDraft;
  onDraftChange: (draft: TaskDraft) => void;
  onSave: (draft?: TaskDraft) => Promise<void>;
}>) {
  const { t } = useTranslation();
  const title = draft.title;
  const dirty = draft.title !== detail.title || draft.body !== detail.body;
  const formShortcut = useTextFieldSubmitShortcut({
    available: canSaveDraft,
    kind: "form",
  });

  function nextTitle(value: string): TaskDraft {
    return { ...draft, title: value.replaceAll("\n", " ") };
  }

  return (
    <form
      className="flex min-w-0 items-center gap-[var(--space-2)]"
      data-testid="task-detail-title-island"
      onKeyDown={formShortcut}
      onSubmit={(event) => {
        event.preventDefault();
        if (canSaveDraft) {
          void onSave();
        }
      }}
    >
      <input
        aria-label={t("task.name")}
        className={cx(
          fieldIslandInputClassName(1, taskDetailIslandRadius),
          "min-w-0 flex-1 px-[var(--space-3)] py-[var(--space-2)] text-[1.125rem] font-bold",
        )}
        disabled={disabled}
        onChange={(event) => {
          onDraftChange(nextTitle(event.target.value));
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !event.metaKey && !event.ctrlKey) {
            event.preventDefault();
            event.currentTarget.form?.requestSubmit();
          }
        }}
        type="text"
        value={title}
      />
      {dirty || detail.actions.canDelete ? (
        <span className="relative block h-9 w-9 shrink-0" data-testid="task-detail-title-action-slot">
          <Button
            aria-label={t("task.save")}
            aria-hidden={!dirty}
            className={`absolute inset-0 transition-opacity motion-reduce:transition-none ${
              dirty ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0 disabled:opacity-0!"
            }`}
            data-testid="task-detail-save"
            disabled={!canSaveDraft || !dirty}
            size="icon"
            tabIndex={dirty ? undefined : -1}
            type="submit"
            variant="primary"
          >
            <Save aria-hidden="true" size={16} strokeWidth={1.75} />
          </Button>
          {detail.actions.canDelete ? <TaskDeleteButton active={!dirty} disabled={disabled} /> : null}
        </span>
      ) : null}
    </form>
  );
}

export function DescriptionIsland({
  disabled,
  draft,
  draftDirty,
  error,
  onDraftChange,
  onPresentationChange,
  onSave,
  presentation,
  submitting,
}: Readonly<{
  disabled: boolean;
  draft: TaskDraft;
  draftDirty: boolean;
  error: unknown;
  onDraftChange: (draft: TaskDraft) => void;
  onPresentationChange: (presentation: DescriptionPresentationState) => void;
  onSave: (draft?: TaskDraft) => Promise<void>;
  presentation: DescriptionPresentationState;
  submitting: boolean;
}>) {
  const { t } = useTranslation();
  const descriptionError = error == null ? undefined : errorMessage(error);
  const submitPolicy = useTextFieldSubmitShortcutPolicy();
  const submitIntent = {
    available: !disabled && !submitting && draft.title.trim().length > 0 && presentation.editing,
    onSubmitIntent: () => {
      if (!draftDirty) {
        onPresentationChange({ ...presentation, editing: false });
        return;
      }
      void onSave(draft)
        .then(() => {
          onPresentationChange({ ...presentation, editing: false });
        })
        .catch(() => {
          return;
        });
    },
    policy: submitPolicy,
  } as const;

  return (
    <div
      className="task-detail-description-island grid h-full w-full min-h-0 min-w-0 max-w-full gap-[var(--space-2)]"
      data-testid="task-description-input-frame"
    >
      <CollapsibleMarkdownField
        collapsedHeightClamp={{ maximumLines: 10, minimumLines: 5, viewportPercent: 50 }}
        disabled={disabled}
        editorMinHeight={220}
        error={descriptionError}
        editing={presentation.editing}
        expandLabel={t("app.expand")}
        expanded={presentation.expanded}
        label={t("task.description")}
        onChange={(body) => {
          onDraftChange({ ...draft, body });
        }}
        onEdit={() => {
          onPresentationChange({ editing: true, expanded: true });
        }}
        onEditingChange={(editing) => {
          onPresentationChange({ ...presentation, editing });
        }}
        onExpand={() => {
          onPresentationChange({ ...presentation, expanded: true });
        }}
        placeholder={t("task.bodyPlaceholder")}
        submitIntent={submitIntent}
        surfaceRadius={taskDetailIslandRadius}
        taskListInteraction={{
          checkedLabel: t("markdown.markIncomplete"),
          uncheckedLabel: t("markdown.markComplete"),
        }}
        value={draft.body}
      />
    </div>
  );
}

export function PropertiesIsland({
  detail,
  disabled,
  mutations,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  const openExternalLink = useOpenExternalLink();
  return (
    <Island
      aria-label={t("task.properties")}
      className="grid min-w-0 content-start gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius={taskDetailIslandRadius}
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
        {detail.currentNodes.map((node) => (
          <TaskCurrentNodeSelectionProperties key={node.nodeID} node={node} />
        ))}
      </dl>
      <TaskActionPanel detail={detail} disabled={disabled} mutations={mutations} />
    </Island>
  );
}

export function TaskCurrentNodeSelectionProperties({ node }: Readonly<{ node: TaskCurrentNode }>) {
  const { t } = useTranslation();
  return (
    <>
      {node.effectiveAssignee === null ? null : (
        <TaskPropertyLine
          label={t("task.currentNodeAssignee", { nodeID: node.nodeID })}
          value={<span className="font-mono">{node.effectiveAssignee}</span>}
        />
      )}
      {node.effectiveThinking === null ? null : (
        <TaskPropertyLine
          label={t("task.currentNodeThinking", { nodeID: node.nodeID })}
          value={<span className="font-mono">{node.effectiveThinking}</span>}
        />
      )}
    </>
  );
}

function TaskActionPanel({
  detail,
  disabled,
  mutations,
}: Readonly<{
  detail: TaskDetail;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  const interruptTarget = taskInterruptTarget(detail);
  const interruptFullLabel =
    interruptTarget === null ? t("board.interrupt") : t("task.interruptChat", { name: interruptTarget });
  const interruptVisibleLabel =
    interruptTarget === null
      ? interruptFullLabel
      : t("task.interruptChat", { name: ellipsizeActionTarget(interruptTarget) });
  return (
    <>
      <div
        className="flex min-w-0 flex-wrap items-center justify-start gap-[var(--space-2)] pt-[var(--space-1)]"
        data-testid="task-detail-action-flow"
      >
        {detail.actions.canStart ? (
          <TaskStartButton disabled={disabled} />
        ) : detail.actions.canResume ? (
          <TaskResumeButton disabled={disabled} />
        ) : null}
        <TaskOpenButtons detail={detail} disabled={disabled} />
        {detail.actions.canInterrupt ? (
          <Button
            aria-label={interruptFullLabel}
            disabled={disabled || mutations.interrupt.isPending}
            onClick={() => {
              mutations.interrupt.mutate(undefined);
            }}
            title={interruptFullLabel}
            variant="danger"
          >
            {interruptVisibleLabel}
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
      {detail.liveSessions.map((session) => {
        const target = taskLiveSessionTarget(session);
        const fullLabel = t("task.openInCli", { name: target });
        return (
          <Button
            aria-label={fullLabel}
            disabled={disabled}
            key={session.sessionID}
            onClick={() => {
              setOpenError("");
              void openInCli(session.sessionID).catch((cause: unknown) => {
                setOpenError(errorMessage(cause));
              });
            }}
            title={fullLabel}
            variant="secondary"
          >
            {t("task.openInCli", { name: ellipsizeActionTarget(target) })}
          </Button>
        );
      })}
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

type TaskLiveSession = TaskDetail["liveSessions"][number];

function taskLiveSessionTarget(session: TaskLiveSession): string {
  if (session.sessionName !== null) {
    return session.sessionName;
  }
  return session.nodeDisplayName.length > 0 ? session.nodeDisplayName : session.sessionID;
}

function taskInterruptTarget(detail: TaskDetail): string | null {
  if (detail.liveSessions.length === 1 && detail.currentScripts.length === 0) {
    const session = detail.liveSessions[0];
    if (session !== undefined) {
      return taskLiveSessionTarget(session);
    }
  }
  return null;
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

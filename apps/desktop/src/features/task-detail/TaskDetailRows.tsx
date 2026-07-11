import { useId, useMemo, useState, type ReactNode } from "react";
import { Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TaskDetail, TaskExecutionTarget, TaskRun } from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppServices } from "../../app/useAppServices";
import { useOpenExternalLink } from "../../app/nativeHooks";
import {
  Button,
  Island,
  MarkdownText,
  Popover,
  PopoverContent,
  PopoverTrigger,
  compactExternalUrlLabel,
  safeExternalUrl,
  showStatusToast,
} from "../../ui";
import { writeClipboardText } from "../../ui/clipboard";
import { cx } from "../../ui/classes";
import { fieldIslandInputClassName } from "../../ui/fieldInputStyles";
import { taskStatusTone } from "./taskStatusTone";
import type { useTaskMutations } from "./useTaskDetailData";
import { useScriptOpenAvailability } from "./useScriptOpenAvailability";

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
}: Readonly<{
  disabled: boolean;
  draft: TaskDraft;
  error: unknown;
  onDraftChange: (draft: TaskDraft) => void;
}>) {
  const { t } = useTranslation();
  const openExternalLink = useOpenExternalLink();
  const [editing, setEditing] = useState(false);
  const descriptionId = useId();
  const descriptionErrorId = `${descriptionId}-error`;
  const descriptionError = error == null ? "" : errorMessage(error);
  const body = draft.body;
  const hasBody = body.trim().length > 0;
  const sharedClassName = cx(fieldIslandInputClassName(1), "block min-h-[220px] min-w-0 p-[var(--space-2)]");
  return (
    <div className="grid min-h-0 min-w-0 gap-[var(--space-2)]" data-testid="task-description-input-frame">
      {editing && !disabled ? (
        <textarea
          aria-describedby={descriptionError.length > 0 ? descriptionErrorId : undefined}
          aria-invalid={descriptionError.length > 0 ? true : undefined}
          aria-label={t("task.description")}
          autoFocus
          className={cx(sharedClassName, "resize-none font-mono")}
          id={descriptionId}
          onBlur={() => {
            setEditing(false);
          }}
          onChange={(event) => {
            onDraftChange({ ...draft, body: event.target.value });
          }}
          placeholder={t("task.bodyPlaceholder")}
          value={body}
        />
      ) : (
        <div
          aria-label={t("task.description")}
          aria-readonly
          className={cx(sharedClassName, !disabled && "cursor-text", "overflow-auto")}
          onFocus={(event) => {
            if (!disabled && event.target === event.currentTarget) {
              setEditing(true);
            }
          }}
          role="textbox"
          tabIndex={disabled ? -1 : 0}
        >
          {hasBody ? (
            <MarkdownText onOpenLink={openExternalLink} value={body} />
          ) : (
            <span className="text-[var(--color-muted)]">{t("task.bodyPlaceholder")}</span>
          )}
        </div>
      )}
      {descriptionError.length > 0 ? (
        <span className="text-[var(--color-error)]" id={descriptionErrorId}>
          {descriptionError}
        </span>
      ) : null}
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
  const { nativeBridge } = useAppServices();
  return (
    <Island
      aria-label={t("task.properties")}
      className="grid min-w-0 content-start gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius="l"
      unpadded
    >
      <PropertyLine
        label={t("task.identifier", { defaultValue: "ID" })}
        value={<span className="font-mono">{detail.shortID}</span>}
      />
      <PropertyLine label={t("task.project")} value={detail.projectName} />
      <PropertyLine
        label={t("task.status")}
        value={<TaskStatusText label={t(`task.statusKinds.${detail.status.kind}`)} tone={taskStatusTone(detail.status)} />}
      />
      <PropertyLine label={t("task.workspace")} value={detail.sourceWorkspace.name} />
      <ExecutionTargetProperties detail={detail} nativeBridge={nativeBridge} />
      <PropertyLine label={t("task.workflow")} value={detail.workflowName} />
      <SourceLine label={t("task.source")} onOpen={openExternalLink} value={detail.sourceURL} />
      <PropertyLine label={t("task.sessions")} value={detail.runs.length.toString()} />
      <TaskActionPanel detail={detail} disabled={disabled} mutations={mutations} />
    </Island>
  );
}

function ExecutionTargetProperties({
  detail,
  nativeBridge,
}: Readonly<{
  detail: TaskDetail;
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"];
}>) {
  const { t } = useTranslation();
  const target = detail.executionTarget;
  if (target === null) {
    return null;
  }
  const source = target.source;
  const root = target.policy === "none" ? detail.sourceWorkspace.rootPath : detail.worktreePath;
  return (
    <>
      <PropertyLine label={t("task.executionTarget")} value={executionTargetPolicyLabel(target.policy, t)} />
      <CopyablePropertyLine label={t("task.executionRoot")} nativeBridge={nativeBridge} value={root} />
      {source?.kind === "named_ref" ? (
        <CopyablePropertyLine label={t("task.executionTargetSourceRef")} nativeBridge={nativeBridge} value={source.namedRef} />
      ) : null}
      {source === null ? null : (
        <CopyablePropertyLine label={t("task.executionTargetCommit")} nativeBridge={nativeBridge} value={source.commit} />
      )}
      {target.policy === "custom_ref" ? (
        <CopyablePropertyLine
          label={t("task.executionTargetRequestedRef")}
          nativeBridge={nativeBridge}
          value={target.customRef}
        />
      ) : null}
    </>
  );
}

function executionTargetPolicyLabel(
  policy: TaskExecutionTarget["policy"],
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (policy === "none") {
    return t("task.executionTargetNone");
  }
  if (policy === "head") {
    return t("task.executionTargetHead");
  }
  if (policy === "default_branch") {
    return t("task.executionTargetDefaultBranch");
  }
  return t("task.executionTargetCustomRef");
}

function CopyablePropertyLine({
  label,
  nativeBridge,
  value,
}: Readonly<{
  label: string;
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"];
  value: string;
}>) {
  const { t } = useTranslation();
  return (
    <PropertyLine
      label={label}
      value={
        <button
          className="-mx-[var(--space-1)] max-w-full truncate rounded-[var(--radius-s)] px-[var(--space-1)] font-mono text-left hover:bg-[var(--color-island-2)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-primary)]"
          onClick={() => {
            void writeClipboardText(value, nativeBridge)
              .then(() => {
                showStatusToast({
                  id: `task-property-copied-${label}`,
                  title: t("task.executionTargetValueCopied", { label }),
                  tone: "success",
                });
              })
              .catch((error: unknown) => {
                showStatusToast({
                  body: errorMessage(error),
                  id: `task-property-copy-failed-${label}`,
                  title: t("task.executionTargetValueCopyFailed", { label }),
                  tone: "danger",
                });
              });
          }}
          title={value}
          type="button"
        >
          {value}
        </button>
      }
    />
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
  const activeRuns = useMemo(
    () => detail.runs.filter((run) => run.completedAt === null && run.interruptedAt === null),
    [detail.runs],
  );
  const interruptableRuns = useMemo(
    () => activeRuns.filter((run) => run.sessionID.trim().length > 0),
    [activeRuns],
  );
  const hasTaskWideInterrupt = useMemo(
    () => activeRuns.some((run) => run.sessionID.trim().length === 0),
    [activeRuns],
  );
  return (
    <>
      <div className="grid gap-[var(--space-2)] pt-[var(--space-1)]">
        <TaskOpenButtons detail={detail} disabled={disabled} />
        {detail.actions.canResume ? (
          <Button
            disabled={disabled}
            onClick={() => {
              void mutations.resume.mutateAsync();
            }}
            variant="primary"
          >
            {t("board.resume")}
          </Button>
        ) : null}
        {detail.actions.canInterrupt && hasTaskWideInterrupt ? (
          <Button
            disabled={disabled}
            onClick={() => {
              void mutations.interrupt.mutateAsync(undefined);
            }}
            variant="secondary"
          >
            {t("board.interrupt")}
          </Button>
        ) : null}
        {detail.actions.canInterrupt
          ? interruptableRuns.map((run) => (
              <Button
                disabled={disabled}
                key={run.id}
                onClick={() => {
                  void mutations.interrupt.mutateAsync(run.sessionID);
                }}
                variant="secondary"
              >
                {t("board.interrupt")} <span>{run.sessionName.trim() || run.sessionID}</span>
              </Button>
            ))
          : null}
        {detail.actions.canCancel ? (
          <Popover>
            <PopoverTrigger asChild>
              <Button disabled={disabled} variant="danger">
                {t("task.cancel")}
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-56" side="top">
              <strong>{t("task.cancelConfirmTitle")}</strong>
              <Button
                disabled={disabled}
                onClick={() => {
                  void mutations.cancel.mutateAsync();
                }}
                variant="danger"
              >
                {t("app.confirm")}
              </Button>
            </PopoverContent>
          </Popover>
        ) : null}
      </div>
    </>
  );
}

function TaskOpenButtons({ detail, disabled }: Readonly<{ detail: TaskDetail; disabled: boolean }>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const [openCliError, setOpenCliError] = useState("");
  const [openScriptError, setOpenScriptError] = useState("");
  const cliSessionExists = useMemo(
    () => detail.runs.some((run) => run.sessionID.trim().length > 0),
    [detail.runs],
  );
  const scriptRun = useMemo(() => preferredScriptRun(detail.runs), [detail.runs]);
  const scriptOpenAvailable = useScriptOpenAvailability({
    scriptPath: scriptRun?.scriptPath ?? "",
    worktreePath: detail.worktreePath,
  });
  const cliCommand = useMemo(() => sessionCommand(detail.runs), [detail.runs]);

  async function openInCli(): Promise<void> {
    if (cliCommand.length === 0) {
      setOpenCliError(t("task.cliCommandUnavailable"));
      return;
    }
    await writeClipboardText(cliCommand, nativeBridge);
    showStatusToast({
      id: "task-cli-command-copied",
      title: t("task.cliCommandCopied"),
      tone: "success",
    });
  }

  async function openScript(): Promise<void> {
    if (scriptRun === null || scriptRun.scriptPath.trim().length === 0) {
      setOpenScriptError(t("task.scriptPathUnavailable"));
      return;
    }
    await nativeBridge.files.openFile({ basePath: detail.worktreePath, path: scriptRun.scriptPath });
  }

  return (
    <>
      {cliSessionExists ? (
        <Button
          disabled={disabled || cliCommand.length === 0}
          onClick={() => {
            setOpenCliError("");
            void openInCli().catch((cause: unknown) => {
              setOpenCliError(errorMessage(cause));
            });
          }}
          variant="secondary"
        >
          {t("task.openInCli")}
        </Button>
      ) : null}
      {scriptOpenAvailable ? (
        <Button
          disabled={disabled || scriptRun === null}
          onClick={() => {
            setOpenScriptError("");
            void openScript().catch((cause: unknown) => {
              setOpenScriptError(errorMessage(cause));
            });
          }}
          variant="secondary"
        >
          {t("task.openScript")}
        </Button>
      ) : null}
      {openCliError.length > 0 ? (
        <p className="m-0 text-sm text-[var(--color-error)]">{openCliError}</p>
      ) : null}
      {openScriptError.length > 0 ? (
        <p className="m-0 text-sm text-[var(--color-error)]">{openScriptError}</p>
      ) : null}
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
    return <PropertyLine label={label} value={trimmed} />;
  }
  return (
    <PropertyLine
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

function PropertyLine({ label, value }: Readonly<{ label: string; value: ReactNode }>) {
  return (
    <p className="m-0 flex min-w-0 flex-wrap items-center gap-[var(--space-1)] text-sm">
      {label}: <span className="text-[var(--color-muted)]">{value}</span>
    </p>
  );
}

function sessionCommand(runs: readonly TaskRun[]): string {
  const run = preferredSessionRun(runs);
  return run === null ? "" : `kent --session=${run.sessionID}`;
}

function preferredSessionRun(runs: readonly TaskRun[]): TaskRun | null {
  const sessionRuns = runs.filter((run) => run.sessionID.trim().length > 0);
  return (
    [...sessionRuns].reverse().find((run) => run.completedAt === null && run.interruptedAt === null) ??
    sessionRuns.at(-1) ??
    null
  );
}

function preferredScriptRun(runs: readonly TaskRun[]): TaskRun | null {
  const scriptRuns = runs.filter((run) => run.nodeKind === "script" && run.scriptPath.trim().length > 0);
  return (
    [...scriptRuns].reverse().find((run) => run.completedAt === null && run.interruptedAt === null) ??
    scriptRuns.at(-1) ??
    null
  );
}

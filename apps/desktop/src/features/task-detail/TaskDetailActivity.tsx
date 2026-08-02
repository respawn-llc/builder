import { Bot, Save, Trash2, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ActivityItem, TaskComment } from "@/api";
import { errorMessage } from "@/api";
import { formatRelativeTime, useStatusController, useTextFieldSubmitShortcut } from "@/app-facade";
import { Button, homeListCardMaxWidthClassName, IslandSurface, MarkdownText } from "@/ui";
import { cx, fieldIslandInputClassName } from "@/ui";
import type { useTaskMutations } from "./useTaskDetailData";

export function CommentComposer({
  body,
  disabled,
  editing,
  mutations,
  onBodyChange,
  onEditingChange,
}: Readonly<{
  body: string;
  disabled: boolean;
  editing: Readonly<{ id: string; body: string }> | null;
  mutations: ReturnType<typeof useTaskMutations>;
  onBodyChange: (body: string) => void;
  onEditingChange: (editing: Readonly<{ id: string; body: string }> | null) => void;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const commentBody = editing?.body ?? body;
  const pending =
    mutations.addComment.isPending || mutations.replaceComment.isPending || mutations.deleteComment.isPending;
  const interactionDisabled = disabled || pending;
  const canSubmit = !interactionDisabled && commentBody.trim().length > 0;

  async function submit(): Promise<void> {
    if (interactionDisabled || commentBody.trim().length === 0) {
      return;
    }
    try {
      if (editing === null) {
        await mutations.addComment.mutateAsync(body);
        onBodyChange("");
        return;
      }
      await mutations.replaceComment.mutateAsync({ commentID: editing.id, body: editing.body });
      onEditingChange(null);
    } catch (error) {
      push({
        id: "task-comment-save-error",
        tone: "danger",
        title: t("task.commentSaveFailed"),
        body: errorMessage(error),
      });
    }
  }
  const submitShortcut = useTextFieldSubmitShortcut({
    action: () => {
      void submit();
    },
    available: canSubmit,
    kind: "direct",
  });

  return (
    <section className="grid gap-[var(--space-2)]">
      <div className="grid" data-testid="task-comment-input-frame">
        <textarea
          aria-label={editing === null ? t("task.addComment") : t("task.editComment")}
          className={cx(
            fieldIslandInputClassName(1),
            "relative z-0 col-start-1 row-start-1 block min-h-[112px] resize-none p-[var(--space-2)] pb-12",
          )}
          disabled={interactionDisabled}
          id="task-comment-body"
          onChange={(event) => {
            if (disabled) {
              return;
            }
            if (editing === null) {
              onBodyChange(event.target.value);
              return;
            }
            onEditingChange({ id: editing.id, body: event.target.value });
          }}
          onKeyDown={submitShortcut}
          placeholder={editing === null ? `${t("task.addComment")}...` : `${t("task.editComment")}...`}
          value={commentBody}
        />
        <Button
          aria-label={editing === null ? t("task.submitComment") : t("task.saveComment")}
          className="relative z-10 col-start-1 row-start-1 self-end justify-self-end"
          data-testid="task-comment-save"
          disabled={!canSubmit}
          onClick={() => void submit()}
          size="icon"
          style={{ marginBottom: "var(--space-2)", marginRight: "var(--space-2)" }}
          variant="primary"
        >
          <Save aria-hidden="true" size={18} strokeWidth={1.8} />
        </Button>
      </div>
    </section>
  );
}

export function CommentRow({
  comment,
  disabled,
  editing,
  mutations,
  onEdit,
  openLink,
}: Readonly<{
  comment: TaskComment;
  disabled: boolean;
  editing: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  onEdit: (comment: TaskComment) => void;
  openLink: (url: string) => void;
}>) {
  const { t } = useTranslation();
  const { push } = useStatusController();
  const pending =
    mutations.addComment.isPending || mutations.replaceComment.isPending || mutations.deleteComment.isPending;
  const interactionDisabled = disabled || pending;
  const authorLabel =
    comment.authorID ??
    t(comment.authorKind === "agent" ? "task.commentAuthorAgent" : "task.commentAuthorUser");

  async function deleteComment(commentID: string): Promise<void> {
    if (interactionDisabled) {
      return;
    }
    try {
      await mutations.deleteComment.mutateAsync(commentID);
    } catch (error) {
      push({
        id: "task-comment-delete-error",
        tone: "danger",
        title: t("task.commentDeleteFailed"),
        body: errorMessage(error),
      });
    }
  }

  return (
    <IslandSurface
      as="article"
      className="grid gap-[var(--space-2)] rounded-[var(--radius-l)] p-[var(--space-2)]"
      level={1}
    >
      <header className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-[var(--space-2)]">
        <CommentAuthorIcon authorKind={comment.authorKind} />
        {editing ? (
          <AuthorText author={authorLabel} />
        ) : (
          <button
            aria-label={t("task.editCommentBy", {
              author: authorLabel,
              defaultValue: `Edit comment by ${authorLabel}`,
            })}
            className="min-w-0 rounded-[var(--radius-m)] p-0 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-primary)]"
            disabled={interactionDisabled}
            onClick={() => {
              onEdit(comment);
            }}
            type="button"
          >
            <AuthorText author={authorLabel} />
          </button>
        )}
        <time className="whitespace-nowrap text-sm text-[var(--color-muted)]">
          {formatRelativeTime(comment.createdAt)}
        </time>
        <button
          aria-label={t("task.deleteComment")}
          className="grid h-8 w-8 place-items-center rounded-full text-[var(--color-error)] transition-colors hover:bg-[color-mix(in_srgb,var(--color-error)_14%,transparent)] disabled:cursor-not-allowed disabled:opacity-55"
          disabled={interactionDisabled}
          onClick={() => void deleteComment(comment.id)}
          type="button"
        >
          <Trash2 aria-hidden="true" size={16} strokeWidth={1.8} />
        </button>
      </header>
      <div className="min-w-0 text-[var(--color-on-island)]">
        <MarkdownText onOpenLink={openLink} value={comment.body} />
      </div>
    </IslandSurface>
  );
}

function CommentAuthorIcon({ authorKind }: Readonly<{ authorKind: TaskComment["authorKind"] }>) {
  return authorKind === "user" ? (
    <UserRound aria-hidden="true" size={16} strokeWidth={1.8} />
  ) : (
    <Bot aria-hidden="true" size={16} strokeWidth={1.8} />
  );
}

function AuthorText({ author }: Readonly<{ author: string }>) {
  return (
    <EllipsisText className="font-bold text-[var(--color-on-island)]" text={author} />
  );
}

function EllipsisText({ className, text }: Readonly<{ className?: string | undefined; text: string }>) {
  return (
    <span className={cx("min-w-0 truncate", className)} title={text}>
      {text}
    </span>
  );
}

export function ActivityRow({
  item,
}: Readonly<{ item: ActivityItem }>) {
  const { t } = useTranslation();
  const summary = item.type === "comment" ? item.comment.body : t("task.sessionStarted");
  return (
    <IslandSurface
      as="article"
      className={cx(
        "grid w-full gap-[var(--space-1)] rounded-[var(--radius-l)] p-[var(--space-2)]",
        homeListCardMaxWidthClassName,
      )}
      level={1}
    >
      <span>{summary}</span>
      <time className="text-sm text-[var(--color-muted)]">{formatRelativeTime(item.occurredAt)}</time>
    </IslandSurface>
  );
}

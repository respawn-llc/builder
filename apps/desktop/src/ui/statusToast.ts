import { createElement, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { toast, type ExternalToast } from "sonner";

export type ToastTone = "neutral" | "info" | "success" | "warning" | "danger";

export type StatusNotice = Readonly<{
  id: string;
  tone: ToastTone;
  title: string;
  body?: string;
  actionLabel?: string;
  onAction?: () => void;
  onClick?: () => void;
  onDismiss?: () => void;
  dismissible?: boolean;
  durationMs?: number;
}>;

type ToastDispatcher = (message: ReactNode, options?: ExternalToast) => string | number;

const toastByTone: Readonly<Record<ToastTone, ToastDispatcher>> = {
  danger: toast.error,
  info: toast.info,
  neutral: toast,
  success: toast.success,
  warning: toast.warning,
};

export function showStatusToast(notice: StatusNotice): void {
  if (notice.onClick !== undefined) {
    toast.custom((toastID) => clickableNoticeContent(notice, toastID), toastOptions(notice, "custom"));
    return;
  }
  toastByTone[notice.tone](toastTitle(notice), toastOptions(notice));
}

function toastOptions(notice: StatusNotice, mode: "custom" | "standard" = "standard"): ExternalToast {
  const duration = notice.dismissible === false ? Infinity : notice.durationMs;
  const action =
    mode === "standard" && notice.actionLabel !== undefined && notice.onAction !== undefined
      ? { action: { label: notice.actionLabel, onClick: notice.onAction } }
      : {};
  const durationOption = duration !== undefined ? { duration } : {};
  const dismissOption = notice.onDismiss !== undefined ? { onDismiss: notice.onDismiss } : {};
  const descriptionOption =
    mode === "custom" || notice.body === undefined || notice.body.length === 0
      ? {}
      : { description: notice.body };
  const options: ExternalToast = {
    ...action,
    ...descriptionOption,
    ...dismissOption,
    ...durationOption,
    closeButton: mode === "standard" && notice.dismissible !== false,
    id: notice.id,
  };
  return options;
}

function toastTitle(notice: StatusNotice): ReactNode {
  return notice.title;
}

export function dismissStatusToast(id: string): void {
  toast.dismiss(id);
}

function clickableNoticeContent(notice: StatusNotice, toastID: string | number) {
  return createElement(
    "article",
    {
      className: "grid cursor-pointer gap-[var(--space-2)] text-left",
      onKeyDown(event: KeyboardEvent<HTMLElement>) {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          notice.onClick?.();
        }
      },
      onClick: notice.onClick,
      role: "button",
      tabIndex: 0,
    },
    createElement(
      "div",
      { className: "grid gap-[var(--space-1)]" },
      createElement("strong", { className: "font-extrabold" }, notice.title),
      notice.body === undefined || notice.body.length === 0
        ? null
        : createElement("span", { className: "text-[var(--color-muted)]" }, notice.body),
    ),
    notice.actionLabel !== undefined && notice.onAction !== undefined
      ? createElement(
          "button",
          {
            className: "justify-self-start rounded-[var(--radius-s)] border border-[var(--color-outline)] px-[var(--space-2)] py-[var(--space-1)]",
            onClick(event: MouseEvent<HTMLButtonElement>) {
              event.stopPropagation();
              notice.onAction?.();
            },
            type: "button",
          },
          notice.actionLabel,
        )
      : null,
    notice.dismissible === false
      ? null
      : createElement(
          "button",
          {
            "aria-label": "Close",
            className: "absolute right-[var(--space-2)] top-[var(--space-2)] rounded-[var(--radius-s)] px-[var(--space-1)]",
            onClick(event: MouseEvent<HTMLButtonElement>) {
              event.stopPropagation();
              toast.dismiss(toastID);
            },
            type: "button",
          },
          "Close",
        ),
  );
}

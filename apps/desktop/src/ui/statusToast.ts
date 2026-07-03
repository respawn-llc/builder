import { createElement, type MouseEvent, type ReactNode } from "react";
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
  toastByTone[notice.tone](toastTitle(notice), toastOptions(notice));
}

function toastOptions(notice: StatusNotice): ExternalToast {
  const duration = notice.dismissible === false ? Infinity : notice.durationMs;
  const action =
    notice.onClick === undefined && notice.actionLabel !== undefined && notice.onAction !== undefined
      ? { action: { label: notice.actionLabel, onClick: notice.onAction } }
      : {};
  const durationOption = duration !== undefined ? { duration } : {};
  const dismissOption = notice.onDismiss !== undefined ? { onDismiss: notice.onDismiss } : {};
  const descriptionOption =
    notice.onClick !== undefined || notice.body === undefined || notice.body.length === 0
      ? {}
      : { description: notice.body };
  const options: ExternalToast = {
    ...action,
    ...descriptionOption,
    ...dismissOption,
    ...durationOption,
    closeButton: notice.dismissible !== false,
    id: notice.id,
  };
  return options;
}

function toastTitle(notice: StatusNotice): ReactNode {
  if (notice.onClick !== undefined) {
    return clickableNoticeTitle(notice);
  }
  return notice.title;
}

export function dismissStatusToast(id: string): void {
  toast.dismiss(id);
}

function clickableNoticeTitle(notice: StatusNotice): ReactNode {
  return createElement(
    "button",
    {
      className:
        "grid w-full min-w-0 cursor-pointer gap-[var(--space-1)] rounded-[var(--radius-s)] border-0 bg-transparent p-0 text-left text-inherit outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-primary)]",
      onClick(event: MouseEvent<HTMLButtonElement>) {
        event.stopPropagation();
        notice.onClick?.();
      },
      type: "button",
    },
    createElement("span", { className: "min-w-0 truncate" }, notice.title),
    notice.body === undefined || notice.body.length === 0
      ? null
      : createElement(
          "span",
          { className: "line-clamp-3 font-normal text-[var(--color-muted)]" },
          notice.body,
        ),
  );
}

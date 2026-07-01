import { toast, type ExternalToast } from "sonner";

export type ToastTone = "neutral" | "info" | "success" | "warning" | "danger";

export type StatusNotice = Readonly<{
  id: string;
  tone: ToastTone;
  title: string;
  body?: string;
  actionLabel?: string;
  onAction?: () => void;
  dismissible?: boolean;
  durationMs?: number;
}>;

export function showStatusToast(notice: StatusNotice): void {
  const duration = notice.dismissible === false ? Infinity : notice.durationMs;
  const action =
    notice.actionLabel !== undefined && notice.onAction !== undefined
      ? { action: { label: notice.actionLabel, onClick: notice.onAction } }
      : {};
  const durationOption = duration !== undefined ? { duration } : {};
  const descriptionOption =
    notice.body === undefined || notice.body.length === 0 ? {} : { description: notice.body };
  const options: ExternalToast = {
    ...action,
    ...descriptionOption,
    ...durationOption,
    closeButton: notice.dismissible !== false,
    id: notice.id,
  };

  switch (notice.tone) {
    case "danger":
      toast.error(notice.title, options);
      return;
    case "info":
      toast.info(notice.title, options);
      return;
    case "success":
      toast.success(notice.title, options);
      return;
    case "warning":
      toast.warning(notice.title, options);
      return;
    case "neutral":
      toast(notice.title, options);
      return;
  }
}

export function dismissStatusToast(id: string): void {
  toast.dismiss(id);
}

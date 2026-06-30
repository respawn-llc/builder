import type { RefObject } from "react";
import type {
  NativeNotification,
  NativeNotificationActivation,
  NativeNotificationTarget,
} from "@app/native-bridge";

import type {
  AttentionItem,
  AttentionNotification,
  AttentionNotificationTaskDetailFocus,
  AttentionNotificationTaskDetailTarget,
} from "../api";
import { RpcError } from "../api/errors";
import type { AppServices } from "./services";
import type { TaskDetailInitialFocus } from "./sidebarContext";
import type { StatusController } from "./statusContextValue";

export type SurfaceRecord = Readonly<{
  notification: AttentionNotification;
  state: "activating" | "activation_failed" | "dismissed" | "native" | "surfacing" | "toast";
}>;

const attentionToastIDPrefix = "attention:";

type NativeNotifications = AppServices["nativeBridge"]["notifications"];
type NativeWindow = AppServices["nativeBridge"]["window"];
type Logger = AppServices["logger"];
type Api = AppServices["api"];
type Translate = (key: string) => string;
type SurfaceOutcome = "done" | "retry";

export async function readCurrentPendingSurface({
  focusedRef,
  id,
  logger,
  surfaced,
  windowControls,
}: Readonly<{
  focusedRef: RefObject<boolean | null>;
  id: string;
  logger: Logger;
  surfaced: ReadonlyMap<string, SurfaceRecord>;
  windowControls: NativeWindow;
}>): Promise<Readonly<{ focused: boolean; record: SurfaceRecord }> | null> {
  if (surfaced.get(id)?.state !== "surfacing") {
    return null;
  }
  const focused = await resolveWindowFocus(windowControls, focusedRef, logger);
  const record = surfaced.get(id);
  if (record?.state !== "surfacing") {
    return null;
  }
  return { focused, record };
}

export async function deliverPendingSurface({
  focused,
  hasNativeNotifications,
  logger,
  notification,
  notifications,
  showToast,
  surfaced,
  t,
}: Readonly<{
  focused: boolean;
  hasNativeNotifications: boolean;
  logger: Logger;
  notification: AttentionNotification;
  notifications: NativeNotifications;
  showToast: (notification: AttentionNotification) => void;
  surfaced: Map<string, SurfaceRecord>;
  t: Translate;
}>): Promise<SurfaceOutcome> {
  if (focused || !hasNativeNotifications) {
    removeActiveNotification(notifications, logger, notification.id);
    showToast(notification);
    return "done";
  }
  return deliverNativePendingSurface({ logger, notification, notifications, showToast, surfaced, t });
}

export async function reconcileActiveSurfaces(
  records: readonly (readonly [string, SurfaceRecord])[],
  api: Api,
  logger: Logger,
): Promise<readonly string[]> {
  const staleIDs: string[] = [];
  for (const [id, record] of records) {
    if (record.notification.target.kind !== "task_detail") {
      continue;
    }
    try {
      const task = await api.getTask(record.notification.target.taskID);
      if (!attentionTargetIsActive(task.attention, record.notification.target)) {
        staleIDs.push(id);
      }
    } catch (error) {
      if (isTaskNotFoundError(error)) {
        staleIDs.push(id);
        continue;
      }
      await logger.append("warn", "Reconciling attention notification state failed.", {
        error: errorMessage(error),
        notificationID: id,
      });
    }
  }
  return staleIDs;
}

export function dismissSurface(
  surfaced: Map<string, SurfaceRecord>,
  status: StatusController,
  id: string,
): void {
  const existing = surfaced.get(id);
  if (existing?.state === "toast") {
    status.dismiss(attentionToastID(id));
  }
  surfaced.delete(id);
}

export function removeActiveNotification(
  notifications: NativeNotifications,
  logger: Logger,
  id: string,
): void {
  void notifications.removeActive(id).catch(async (error: unknown) => {
    await logger.append("warn", "Removing native attention notification failed.", {
      error: errorMessage(error),
      notificationID: id,
    });
  });
}

export async function openNativeActivation(
  activation: NativeNotificationActivation,
  openTarget: (target: NativeNotificationTarget) => Promise<void>,
  logger: Logger,
): Promise<void> {
  try {
    await openTarget(activation.target);
  } catch (error) {
    await logger.append("warn", "Opening native attention notification target failed.", {
      error: errorMessage(error),
      notificationID: activation.id,
    });
  }
}

export function taskDetailInitialFocus(focus: AttentionNotificationTaskDetailFocus): TaskDetailInitialFocus {
  if (focus.kind === "question") {
    return { kind: "question", askIDs: focus.askIDs };
  }
  return { kind: "approval", taskTransitionID: focus.taskTransitionID };
}

export function notificationTitle(notification: AttentionNotification, t: Translate): string {
  if (notification.presentation.title.length > 0) {
    return notification.presentation.title;
  }
  const shortID = notification.target.kind === "task_detail" ? notification.target.taskShortID : "";
  const suffix =
    notification.kind === "question" ? t("app.attention.questionTitle") : t("app.attention.approvalTitle");
  return shortID.length > 0 ? `${shortID}: ${suffix}` : suffix;
}

export function notificationBody(notification: AttentionNotification, t: Translate): string {
  if (notification.presentation.body.length > 0) {
    return notification.presentation.body;
  }
  if (notification.presentation.fallbackBody.length > 0) {
    return notification.presentation.fallbackBody;
  }
  return notification.kind === "question"
    ? t("app.attention.questionFallback")
    : t("app.attention.approvalFallback");
}

export function attentionToastID(id: string): string {
  return `${attentionToastIDPrefix}${id}`;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

async function resolveWindowFocus(
  windowControls: NativeWindow,
  focusedRef: RefObject<boolean | null>,
  logger: Logger,
): Promise<boolean> {
  if (focusedRef.current !== null) {
    return focusedRef.current;
  }
  try {
    const focused = await windowControls.isFocused();
    focusedRef.current = focused;
    return focused;
  } catch (error) {
    await logger.append("warn", "Reading native window focus state failed.", {
      error: errorMessage(error),
    });
    focusedRef.current = false;
    return false;
  }
}

async function deliverNativePendingSurface({
  logger,
  notification,
  notifications,
  showToast,
  surfaced,
  t,
}: Readonly<{
  logger: Logger;
  notification: AttentionNotification;
  notifications: NativeNotifications;
  showToast: (notification: AttentionNotification) => void;
  surfaced: Map<string, SurfaceRecord>;
  t: Translate;
}>): Promise<SurfaceOutcome> {
  try {
    await notifications.notify(nativeNotification(notification, t));
  } catch (error) {
    await handleNativeDeliveryError({ error, logger, notification, showToast, surfaced });
    return "done";
  }
  const latest = surfaced.get(notification.id);
  if (latest?.state !== "surfacing") {
    removeActiveNotification(notifications, logger, notification.id);
    return "done";
  }
  if (latest.notification.revision !== notification.revision) {
    return "retry";
  }
  surfaced.set(notification.id, { notification, state: "native" });
  return "done";
}

async function handleNativeDeliveryError({
  error,
  logger,
  notification,
  showToast,
  surfaced,
}: Readonly<{
  error: unknown;
  logger: Logger;
  notification: AttentionNotification;
  showToast: (notification: AttentionNotification) => void;
  surfaced: Map<string, SurfaceRecord>;
}>): Promise<void> {
  const latest = surfaced.get(notification.id);
  if (latest?.state !== "surfacing") {
    return;
  }
  await logger.append("warn", "Native attention notification delivery failed.", {
    error: errorMessage(error),
    notificationID: notification.id,
  });
  showToast(latest.notification);
}

function attentionTargetIsActive(
  attention: readonly AttentionItem[],
  target: AttentionNotificationTaskDetailTarget,
): boolean {
  const { focus } = target;
  if (focus.kind === "question") {
    const askIDs = new Set(focus.askIDs);
    return attention.some(
      (item) =>
        item.kind === "question" &&
        item.runID === target.runID &&
        item.sessionID === target.sessionID &&
        askIDs.has(item.askID),
    );
  }
  return attention.some(
    (item) => item.kind === "approval" && item.taskTransitionID === focus.taskTransitionID,
  );
}

function nativeNotification(notification: AttentionNotification, t: Translate): NativeNotification {
  const target = nativeTarget(notification.target);
  if (target === null) {
    throw new Error("Attention notification target is not native-openable.");
  }
  return {
    id: notification.id,
    title: notificationTitle(notification, t),
    body: notificationBody(notification, t),
    target,
  };
}

function nativeTarget(target: AttentionNotification["target"]): NativeNotificationTarget | null {
  if (target.kind !== "task_detail") {
    return null;
  }
  const focus = taskDetailInitialFocus(target.focus);
  if (focus.kind === "question" && focus.askIDs.length === 0) {
    return null;
  }
  return {
    kind: "task_detail",
    taskID: target.taskID,
    focus:
      focus.kind === "question"
        ? { kind: "question", askIDs: [focus.askIDs[0] ?? "", ...focus.askIDs.slice(1)] }
        : focus,
  };
}

function isTaskNotFoundError(error: unknown): boolean {
  return error instanceof RpcError && error.message === "workflow task not found";
}

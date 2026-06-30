import { useCallback, useEffect, useRef, type RefObject } from "react";
import { useTranslation } from "react-i18next";
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
import type { RpcSubscription } from "../api/transport";
import type { TaskDetailInitialFocus } from "./sidebarContext";
import { useAppServices } from "./useAppServices";
import { useConnectionSnapshot } from "./useConnectionSnapshot";
import { useSidebar } from "./sidebarContext";
import { useStatusController } from "./useStatusController";

type SurfaceRecord = Readonly<{
  notification: AttentionNotification;
  state: "activating" | "activation_failed" | "dismissed" | "native" | "surfacing" | "toast";
}>;

const attentionToastIDPrefix = "attention:";

export function AttentionNotificationController() {
  const { t } = useTranslation();
  const { api, logger, nativeBridge: bridge } = useAppServices();
  const { openSidebar } = useSidebar();
  const status = useStatusController();
  const connection = useConnectionSnapshot();
  const focusedRef = useRef<boolean | null>(null);
  const reconciledGenerationRef = useRef(connection.generation);
  const surfacedRef = useRef(new Map<string, SurfaceRecord>());

  const openTarget = useCallback(
    async (target: AttentionNotificationTaskDetailTarget | NativeNotificationTarget): Promise<void> => {
      try {
        await bridge.window.focusMain();
      } catch (error) {
        await logger.append("warn", "Focusing the main window for attention failed.", {
          error: errorMessage(error),
        });
      }
      try {
        await openSidebar({
          kind: "taskDetail",
          initialFocus: taskDetailInitialFocus(target.focus),
          inboxNav: true,
          mode: "overlay",
          taskID: target.taskID,
        });
      } catch (error) {
        await logger.append("warn", "Opening attention notification target failed.", {
          error: errorMessage(error),
          taskID: target.taskID,
        });
        throw error;
      }
    },
    [bridge.window, logger, openSidebar],
  );

  const showAttentionToast = useCallback(
    (notification: AttentionNotification): void => {
      if (notification.target.kind !== "task_detail") {
        return;
      }
      const target = notification.target;
      const toastID = attentionToastID(notification.id);
      const markDismissed = () => {
        const existing = surfacedRef.current.get(notification.id);
        if (existing !== undefined) {
          surfacedRef.current.set(notification.id, {
            notification: existing.notification,
            state: "dismissed",
          });
        }
      };
      const activate = () => {
        status.dismiss(toastID);
        surfacedRef.current.set(notification.id, { notification, state: "activating" });
        void openTarget(target)
          .then(() => {
            const current = surfacedRef.current.get(notification.id);
            if (current?.state === "activating") {
              surfacedRef.current.set(notification.id, {
                notification: current.notification,
                state: "dismissed",
              });
            }
          })
          .catch(() => {
            const current = surfacedRef.current.get(notification.id);
            if (current?.state === "activating") {
              surfacedRef.current.set(notification.id, {
                notification: current.notification,
                state: "activation_failed",
              });
            }
          });
      };
      status.push({
        id: toastID,
        tone: "info",
        title: notificationTitle(notification, t),
        body: notificationBody(notification, t),
        actionLabel: t("app.attention.open"),
        onAction: activate,
        onClick: activate,
        onDismiss: markDismissed,
        durationMs: Infinity,
      });
      surfacedRef.current.set(notification.id, { notification, state: "toast" });
    },
    [openTarget, status, t],
  );

  const surfaceCurrentPending = useCallback(
    async (id: string): Promise<void> => {
      for (;;) {
        const pending = await readCurrentPendingSurface({
          focusedRef,
          id,
          logger,
          surfaced: surfacedRef.current,
          windowControls: bridge.window,
        });
        if (pending === null) {
          return;
        }
        const outcome = await deliverPendingSurface({
          focused: pending.focused,
          hasNativeNotifications: bridge.capabilities.notifications.basic,
          logger,
          notification: pending.record.notification,
          notifications: bridge.notifications,
          showToast: showAttentionToast,
          surfaced: surfacedRef.current,
          t,
        });
        if (outcome !== "retry") {
          return;
        }
      }
    },
    [
      bridge.capabilities.notifications.basic,
      bridge.notifications,
      bridge.window,
      logger,
      showAttentionToast,
      t,
    ],
  );

  const handlePending = useCallback(
    async (notification: AttentionNotification): Promise<void> => {
      if (notification.target.kind !== "task_detail") {
        return;
      }
      const existing = surfacedRef.current.get(notification.id);
      if (existing !== undefined) {
        if (existing.state === "activation_failed" || existing.state === "toast") {
          showAttentionToast(notification);
          return;
        }
        surfacedRef.current.set(notification.id, { notification, state: existing.state });
        if (existing.state === "native") {
          surfacedRef.current.set(notification.id, { notification, state: "surfacing" });
          await surfaceCurrentPending(notification.id);
        }
        return;
      }
      surfacedRef.current.set(notification.id, { notification, state: "surfacing" });
      await surfaceCurrentPending(notification.id);
    },
    [showAttentionToast, surfaceCurrentPending],
  );

  const handleResolved = useCallback(
    (id: string): void => {
      dismissSurface(surfacedRef.current, status, id);
      removeActiveNotification(bridge.notifications, logger, id);
    },
    [bridge.notifications, logger, status],
  );

  const reconcileSurfacedNotifications = useCallback((): void => {
    const records = [...surfacedRef.current.entries()];
    if (records.length === 0) {
      return;
    }
    void reconcileActiveSurfaces(records, api, logger).then((staleIDs) => {
      for (const id of staleIDs) {
        dismissSurface(surfacedRef.current, status, id);
        removeActiveNotification(bridge.notifications, logger, id);
      }
    });
  }, [api, bridge.notifications, logger, status]);

  useEffect(() => {
    let active = true;
    void bridge.window
      .isFocused()
      .then((focused) => {
        if (active) {
          focusedRef.current = focused;
        }
      })
      .catch(async (error: unknown) => {
        await logger.append("warn", "Reading native window focus state failed.", {
          error: errorMessage(error),
        });
      });
    let unlisten: (() => void) | null = null;
    void bridge.window
      .onFocusChanged((focused) => {
        focusedRef.current = focused;
      })
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
        } else {
          nextUnlisten();
        }
      })
      .catch(async (error: unknown) => {
        await logger.append("warn", "Listening for native window focus changes failed.", {
          error: errorMessage(error),
        });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [bridge.window, logger]);

  useEffect(() => {
    if (!bridge.capabilities.notifications.basic) {
      return;
    }
    void bridge.notifications
      .permissionState()
      .then(async (permission) => {
        const resolvedPermission =
          permission === "prompt" ? await bridge.notifications.requestPermission() : permission;
        if (resolvedPermission === "denied") {
          status.push({
            id: "attention-native-permission-denied",
            tone: "warning",
            title: t("app.attention.permissionDeniedTitle"),
            body: t("app.attention.permissionDeniedBody"),
          });
        }
      })
      .catch(async (error: unknown) => {
        await logger.append("warn", "Reading native notification permission failed.", {
          error: errorMessage(error),
        });
      });
  }, [bridge.capabilities.notifications.basic, bridge.notifications, logger, status, t]);

  useEffect(() => {
    if (connection.phase !== "connected" || connection.generation === reconciledGenerationRef.current) {
      return;
    }
    reconciledGenerationRef.current = connection.generation;
    reconcileSurfacedNotifications();
  }, [connection.generation, connection.phase, reconcileSurfacedNotifications]);

  useEffect(() => {
    let subscription: RpcSubscription | null = api.subscribeAttentionNotifications({
      onEvent(event) {
        if (event.type === "pending") {
          void handlePending(event.pending);
          return;
        }
        if (event.type === "resolved") {
          handleResolved(event.id);
        }
      },
      onComplete(code) {
        if (code === 0) {
          subscription = null;
        }
      },
      onError(error) {
        void logger.append("warn", "Attention notification stream failed.", {
          error: errorMessage(error),
        });
        reconcileSurfacedNotifications();
      },
    });
    return () => {
      subscription?.close();
    };
  }, [api, handlePending, handleResolved, logger, reconcileSurfacedNotifications]);

  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let active = true;
    void bridge.notifications
      .onActivated((activation: NativeNotificationActivation) => {
        void openNativeActivation(activation, openTarget, logger);
      })
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
        } else {
          nextUnlisten();
        }
      })
      .catch(async (error: unknown) => {
        await logger.append("warn", "Listening for native attention notification activation failed.", {
          error: errorMessage(error),
        });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [bridge.notifications, logger, openTarget]);

  return null;
}

async function resolveWindowFocus(
  windowControls: ReturnType<typeof useAppServices>["nativeBridge"]["window"],
  focusedRef: RefObject<boolean | null>,
  logger: ReturnType<typeof useAppServices>["logger"],
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

async function readCurrentPendingSurface({
  focusedRef,
  id,
  logger,
  surfaced,
  windowControls,
}: Readonly<{
  focusedRef: RefObject<boolean | null>;
  id: string;
  logger: ReturnType<typeof useAppServices>["logger"];
  surfaced: ReadonlyMap<string, SurfaceRecord>;
  windowControls: ReturnType<typeof useAppServices>["nativeBridge"]["window"];
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

async function deliverPendingSurface({
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
  logger: ReturnType<typeof useAppServices>["logger"];
  notification: AttentionNotification;
  notifications: ReturnType<typeof useAppServices>["nativeBridge"]["notifications"];
  showToast: (notification: AttentionNotification) => void;
  surfaced: Map<string, SurfaceRecord>;
  t: (key: string) => string;
}>): Promise<"done" | "retry"> {
  if (focused || !hasNativeNotifications) {
    removeActiveNotification(notifications, logger, notification.id);
    showToast(notification);
    return "done";
  }
  return deliverNativePendingSurface({ logger, notification, notifications, showToast, surfaced, t });
}

async function deliverNativePendingSurface({
  logger,
  notification,
  notifications,
  showToast,
  surfaced,
  t,
}: Readonly<{
  logger: ReturnType<typeof useAppServices>["logger"];
  notification: AttentionNotification;
  notifications: ReturnType<typeof useAppServices>["nativeBridge"]["notifications"];
  showToast: (notification: AttentionNotification) => void;
  surfaced: Map<string, SurfaceRecord>;
  t: (key: string) => string;
}>): Promise<"done" | "retry"> {
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
  logger: ReturnType<typeof useAppServices>["logger"];
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

async function reconcileActiveSurfaces(
  records: readonly (readonly [string, SurfaceRecord])[],
  api: ReturnType<typeof useAppServices>["api"],
  logger: ReturnType<typeof useAppServices>["logger"],
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

function isTaskNotFoundError(error: unknown): boolean {
  return error instanceof RpcError && error.message === "workflow task not found";
}

function dismissSurface(
  surfaced: Map<string, SurfaceRecord>,
  status: ReturnType<typeof useStatusController>,
  id: string,
): void {
  const existing = surfaced.get(id);
  if (existing?.state === "toast") {
    status.dismiss(attentionToastID(id));
  }
  surfaced.delete(id);
}

function removeActiveNotification(
  notifications: ReturnType<typeof useAppServices>["nativeBridge"]["notifications"],
  logger: ReturnType<typeof useAppServices>["logger"],
  id: string,
): void {
  void notifications.removeActive(id).catch(async (error: unknown) => {
    await logger.append("warn", "Removing native attention notification failed.", {
      error: errorMessage(error),
      notificationID: id,
    });
  });
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
    (item) =>
      item.kind === "approval" &&
      item.runID === target.runID &&
      item.sessionID === target.sessionID &&
      item.taskTransitionID === focus.taskTransitionID,
  );
}

async function openNativeActivation(
  activation: NativeNotificationActivation,
  openTarget: (target: NativeNotificationTarget) => Promise<void>,
  logger: ReturnType<typeof useAppServices>["logger"],
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

function nativeNotification(
  notification: AttentionNotification,
  t: (key: string) => string,
): NativeNotification {
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

function taskDetailInitialFocus(focus: AttentionNotificationTaskDetailFocus): TaskDetailInitialFocus {
  if (focus.kind === "question") {
    return { kind: "question", askIDs: focus.askIDs };
  }
  return { kind: "approval", taskTransitionID: focus.taskTransitionID };
}

function notificationTitle(notification: AttentionNotification, t: (key: string) => string): string {
  if (notification.presentation.title.length > 0) {
    return notification.presentation.title;
  }
  const shortID = notification.target.kind === "task_detail" ? notification.target.taskShortID : "";
  const suffix =
    notification.kind === "question" ? t("app.attention.questionTitle") : t("app.attention.approvalTitle");
  return shortID.length > 0 ? `${shortID}: ${suffix}` : suffix;
}

function notificationBody(notification: AttentionNotification, t: (key: string) => string): string {
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

function attentionToastID(id: string): string {
  return `${attentionToastIDPrefix}${id}`;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

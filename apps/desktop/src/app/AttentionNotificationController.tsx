import { useCallback, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { NativeNotificationActivation, NativeNotificationTarget } from "@app/native-bridge";

import type {
  AttentionNotification,
  AttentionNotificationID,
  AttentionNotificationWorkflowTaskTarget,
} from "../api";
import { errorMessage } from "../api/errors";
import type { RpcSubscription } from "../api/transport";
import {
  attentionToastID,
  attentionNotificationIDKey,
  deliverPendingSurface,
  dismissSurface,
  notificationBody,
  notificationTitle,
  openNativeActivation,
  readCurrentPendingSurface,
  reconcileActiveSurfaces,
  removeActiveNotification,
  taskDetailInitialFocus,
  type SurfaceRecord,
} from "./attentionNotificationSurfaces";
import { useAppServices } from "./useAppServices";
import { useConnectionSnapshot } from "./useConnectionSnapshot";
import { useSidebar } from "./sidebarContext";
import { useStatusController } from "./useStatusController";

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
    async (target: AttentionNotificationWorkflowTaskTarget | NativeNotificationTarget): Promise<void> => {
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
      if (notification.target.kind !== "workflow_task") {
        return;
      }
      const target = notification.target;
      const notificationKey = attentionNotificationIDKey(notification.id);
      const toastID = attentionToastID(notificationKey);
      const markDismissed = () => {
        const existing = surfacedRef.current.get(notificationKey);
        if (existing?.state === "toast") {
          surfacedRef.current.set(notificationKey, {
            notification: existing.notification,
            state: "dismissed",
          });
        }
      };
      const activate = () => {
        surfacedRef.current.set(notificationKey, { notification, state: "activating" });
        status.dismiss(toastID);
        void openTarget(target)
          .then(() => {
            const current = surfacedRef.current.get(notificationKey);
            if (current?.state === "activating") {
              surfacedRef.current.set(notificationKey, {
                notification: current.notification,
                state: "dismissed",
              });
            }
          })
          .catch(() => {
            const current = surfacedRef.current.get(notificationKey);
            if (current?.state === "activating") {
              surfacedRef.current.set(notificationKey, {
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
      surfacedRef.current.set(notificationKey, { notification, state: "toast" });
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
        if (outcome.status !== "retry") {
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
      if (notification.target.kind !== "workflow_task") {
        return;
      }
      const notificationKey = attentionNotificationIDKey(notification.id);
      const existing = surfacedRef.current.get(notificationKey);
      if (existing !== undefined) {
        if (existing.state === "activation_failed" || existing.state === "toast") {
          showAttentionToast(notification);
          return;
        }
        surfacedRef.current.set(notificationKey, { notification, state: existing.state });
        if (existing.state === "native") {
          surfacedRef.current.set(notificationKey, { notification, state: "surfacing" });
          await surfaceCurrentPending(notificationKey);
        }
        return;
      }
      surfacedRef.current.set(notificationKey, { notification, state: "surfacing" });
      await surfaceCurrentPending(notificationKey);
    },
    [showAttentionToast, surfaceCurrentPending],
  );

  const handleResolved = useCallback(
    (id: AttentionNotificationID): void => {
      const notificationKey = attentionNotificationIDKey(id);
      dismissSurface(surfacedRef.current, status, notificationKey);
      removeActiveNotification(bridge.notifications, logger, notificationKey);
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

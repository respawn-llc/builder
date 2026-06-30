import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import {
  isPermissionGranted as tauriIsPermissionGranted,
  requestPermission as tauriRequestPermission,
} from "@tauri-apps/plugin-notification";

import { tauriPlatformSupportsNativeNotifications, type NativePlatform } from "./capabilities";
import { NativeNotificationIDMapper } from "./notificationIds";

export type NativeNotificationPermission = "denied" | "granted" | "prompt" | "unsupported";

export type NativeNotificationQuestionFocus = Readonly<{
  kind: "question";
  askIDs: readonly [string, ...string[]];
}>;

export type NativeNotificationApprovalFocus = Readonly<{
  kind: "approval";
  taskTransitionID: string;
}>;

export type NativeNotificationTaskDetailTarget = Readonly<{
  kind: "task_detail";
  taskID: string;
  focus: NativeNotificationQuestionFocus | NativeNotificationApprovalFocus;
}>;

export type NativeNotificationTarget = NativeNotificationTaskDetailTarget;

export type NativeNotification = Readonly<{
  id: string;
  title: string;
  body: string;
  target: NativeNotificationTarget;
}>;

export type NativeNotificationActivation = Readonly<{
  id: string;
  target: NativeNotificationTarget;
}>;

export type NativeNotificationUnlisten = () => void;

export type NativeNotificationBridge = Readonly<{
  permissionState(): Promise<NativeNotificationPermission>;
  requestPermission(): Promise<NativeNotificationPermission>;
  notify(message: NativeNotification): Promise<void>;
  onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<NativeNotificationUnlisten>;
  removeActive(id: string): Promise<void>;
}>;

type WebNotificationInstance = Readonly<{
  close(): void;
}> & {
  onclick?: ((event: Event) => void) | null;
};

interface WebNotificationConstructor {
  permission: NotificationPermission;
  requestPermission(): Promise<NotificationPermission>;
  new (title: string, options?: NotificationOptions): WebNotificationInstance;
}

type WebNotificationRuntime = Readonly<{
  notification: WebNotificationConstructor | null;
  focusWindow(): void;
}>;

type TauriNotificationRequest = Readonly<{
  backendId: number;
  id: string;
  title: string;
  body: string;
  target: NativeNotificationTarget;
}>;

type TauriNotificationBackend = Readonly<{
  isPermissionGranted(): Promise<boolean>;
  requestPermission(): Promise<NotificationPermission>;
  send(notification: TauriNotificationRequest): Promise<void>;
  onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<NativeNotificationUnlisten>;
  removeActive(backendID: number): Promise<void>;
}>;

const tauriActivationEvent = "app://attention-notification-activated";

export function createUnavailableNativeNotifications(): NativeNotificationBridge {
  return {
    async permissionState(): Promise<NativeNotificationPermission> {
      return "unsupported";
    },
    async requestPermission(): Promise<NativeNotificationPermission> {
      return "unsupported";
    },
    async notify(): Promise<void> {
      throw new Error("Native notifications are unavailable in this shell.");
    },
    async onActivated(): Promise<NativeNotificationUnlisten> {
      return () => undefined;
    },
    async removeActive(): Promise<void> {
      return Promise.resolve();
    },
  };
}

export type TauriNativeNotificationOptions = Readonly<{
  platform: NativePlatform;
  backend?: TauriNotificationBackend;
}>;

export function createTauriNativeNotifications(options: TauriNativeNotificationOptions): NativeNotificationBridge {
  if (!tauriNotificationsSupported(options.platform)) {
    return createUnavailableNativeNotifications();
  }
  return createTauriDesktopNativeNotifications(options.backend ?? defaultTauriNotificationBackend());
}

export function createBrowserNativeNotifications(
  runtime: WebNotificationRuntime = globalWebNotificationRuntime(),
): NativeNotificationBridge {
  return createWebNativeNotifications(runtime);
}

export function parseNativeNotificationActivationPayload(
  payload: unknown,
): NativeNotificationActivation {
  const record = requireRecord(payload, "Native notification activation payload");
  return {
    id: requireNonEmptyString(record.id, "Native notification activation id"),
    target: parseNativeNotificationTarget(record.target),
  };
}

export function validateNativeNotification(message: NativeNotification): void {
  requireNonEmptyString(message.id, "Native notification id");
  requireNonEmptyString(message.title, "Native notification title");
  requireNonEmptyString(message.body, "Native notification body");
  parseNativeNotificationTarget(message.target);
}

function createWebNativeNotifications(runtime: WebNotificationRuntime): NativeNotificationBridge {
  const handlers = new Set<(activation: NativeNotificationActivation) => void>();
  const activeNotifications = new Map<string, WebNotificationInstance>();
  const mapper = new NativeNotificationIDMapper();
  return {
    async permissionState(): Promise<NativeNotificationPermission> {
      return readWebPermission(runtime.notification);
    },
    async requestPermission(): Promise<NativeNotificationPermission> {
      if (runtime.notification === null) {
        return "unsupported";
      }
      return webPermission(await runtime.notification.requestPermission());
    },
    async notify(message: NativeNotification): Promise<void> {
      validateNativeNotification(message);
      if (runtime.notification === null) {
        throw new Error("Native notifications are unavailable in this shell.");
      }
      const NotificationConstructor = runtime.notification;
      const permission = readWebPermission(NotificationConstructor);
      if (permission !== "granted") {
        throw new Error(`Native notification permission is ${permission}.`);
      }
      const activation = parseNativeNotificationActivationPayload(message);
      activeNotifications.get(message.id)?.close();
      mapper.resolveBackendID(message.id);
      const notification = new NotificationConstructor(message.title, {
        body: message.body,
        data: activation,
        requireInteraction: true,
        tag: message.id,
      });
      activeNotifications.set(message.id, notification);
      notification.onclick = () => {
        runtime.focusWindow();
        activeNotifications.get(message.id)?.close();
        activeNotifications.delete(message.id);
        handlers.forEach((handler) => {
          handler(activation);
        });
      };
    },
    async onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<NativeNotificationUnlisten> {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
    async removeActive(id: string): Promise<void> {
      activeNotifications.get(id)?.close();
      activeNotifications.delete(id);
    },
  };
}

function createTauriDesktopNativeNotifications(backend: TauriNotificationBackend): NativeNotificationBridge {
  const mapper = new NativeNotificationIDMapper();
  return {
    async permissionState(): Promise<NativeNotificationPermission> {
      return (await backend.isPermissionGranted()) ? "granted" : "prompt";
    },
    async requestPermission(): Promise<NativeNotificationPermission> {
      return webPermission(await backend.requestPermission());
    },
    async notify(message: NativeNotification): Promise<void> {
      validateNativeNotification(message);
      if (!(await backend.isPermissionGranted())) {
        throw new Error("Native notification permission is not granted.");
      }
      await backend.send({
        backendId: mapper.resolveBackendID(message.id),
        body: message.body,
        id: message.id,
        target: parseNativeNotificationTarget(message.target),
        title: message.title,
      });
    },
    onActivated: backend.onActivated,
    async removeActive(id: string): Promise<void> {
      await backend.removeActive(mapper.resolveBackendID(id));
    },
  };
}

function defaultTauriNotificationBackend(): TauriNotificationBackend {
  return {
    isPermissionGranted: tauriIsPermissionGranted,
    async onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<NativeNotificationUnlisten> {
      return listen<NativeNotificationActivation>(tauriActivationEvent, (event) => {
        handler(parseNativeNotificationActivationPayload(event.payload));
      });
    },
    async removeActive(backendID: number): Promise<void> {
      await invoke("remove_attention_notification", { backendId: backendID });
    },
    requestPermission: tauriRequestPermission,
    async send(notification: TauriNotificationRequest): Promise<void> {
      await invoke("send_attention_notification", { notification });
    },
  };
}

function parseNativeNotificationTarget(value: unknown): NativeNotificationTarget {
  const record = requireRecord(value, "Native notification target");
  const kind = requireNonEmptyString(record.kind, "Native notification target kind");
  if (kind !== "task_detail") {
    throw new Error(`Unsupported native notification target kind: ${kind}.`);
  }
  return {
    kind,
    taskID: requireNonEmptyString(record.taskID, "Native notification task id"),
    focus: parseNativeNotificationFocus(record.focus),
  };
}

function parseNativeNotificationFocus(
  value: unknown,
): NativeNotificationQuestionFocus | NativeNotificationApprovalFocus {
  const record = requireRecord(value, "Native notification focus");
  const kind = requireNonEmptyString(record.kind, "Native notification focus kind");
  if (kind === "question") {
    const askIDs = parseNonEmptyStringArray(record.askIDs, "Native notification ask ids");
    return { kind, askIDs };
  }
  if (kind === "approval") {
    return {
      kind,
      taskTransitionID: requireNonEmptyString(
        record.taskTransitionID,
        "Native notification task transition id",
      ),
    };
  }
  throw new Error(`Unsupported native notification focus kind: ${kind}.`);
}

function parseNonEmptyStringArray(
  value: unknown,
  fieldName: string,
): readonly [string, ...string[]] {
  if (!isNonEmptyArray(value)) {
    throw new Error(`${fieldName} must be a non-empty string array.`);
  }
  const [first, ...rest] = value;
  return [
    requireNonEmptyString(first, fieldName),
    ...rest.map((entry) => requireNonEmptyString(entry, fieldName)),
  ];
}

function readWebPermission(
  notification: WebNotificationConstructor | null,
): NativeNotificationPermission {
  if (notification === null) {
    return "unsupported";
  }
  return webPermission(notification.permission);
}

function webPermission(permission: NotificationPermission): NativeNotificationPermission {
  if (permission === "default") {
    return "prompt";
  }
  return permission;
}

function globalWebNotificationRuntime(): WebNotificationRuntime {
  return {
    notification: globalNotificationConstructor(),
    focusWindow() {
      if (typeof window !== "undefined") {
        window.focus();
      }
    },
  };
}

function globalNotificationConstructor(): WebNotificationConstructor | null {
  if (typeof globalThis === "undefined" || !("Notification" in globalThis)) {
    return null;
  }
  return globalThis.Notification;
}

function requireRecord(value: unknown, fieldName: string): Record<string, unknown> {
  if (!isRecord(value)) {
    throw new Error(`${fieldName} must be an object.`);
  }
  return value;
}

function requireNonEmptyString(value: unknown, fieldName: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${fieldName} must be a non-empty string.`);
  }
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isNonEmptyArray(value: unknown): value is readonly [unknown, ...unknown[]] {
  return Array.isArray(value) && value.length > 0;
}

function tauriNotificationsSupported(platform: NativePlatform): boolean {
  return tauriPlatformSupportsNativeNotifications(platform);
}

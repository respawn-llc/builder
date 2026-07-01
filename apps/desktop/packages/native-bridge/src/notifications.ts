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

export type NativeNotificationInterruptedRunFocus = Readonly<{
  kind: "interrupted_run";
  runID: string;
}>;

export type NativeNotificationTaskDetailTarget = Readonly<{
  kind: "task_detail";
  taskID: string;
  focus:
    | NativeNotificationQuestionFocus
    | NativeNotificationApprovalFocus
    | NativeNotificationInterruptedRunFocus;
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
  if (!tauriPlatformSupportsNativeNotifications(options.platform)) {
    return createUnavailableNativeNotifications();
  }
  return createTauriDesktopNativeNotifications(options.backend ?? defaultTauriNotificationBackend());
}

export function createBrowserNativeNotifications(
  runtime: WebNotificationRuntime = globalWebNotificationRuntime(),
): NativeNotificationBridge {
  return createWebNativeNotifications(runtime);
}

export function validateNativeNotification(message: NativeNotification): void {
  if (message.id.length === 0) {
    throw new Error("Native notification id must be a non-empty string.");
  }
  if (message.title.length === 0) {
    throw new Error("Native notification title must be a non-empty string.");
  }
  if (message.body.length === 0) {
    throw new Error("Native notification body must be a non-empty string.");
  }
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
      const activation: NativeNotificationActivation = {
        id: message.id,
        target: message.target,
      };
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
        target: message.target,
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
      return listen<unknown>(tauriActivationEvent, (event) => {
        handler(nativeNotificationActivation(event.payload));
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

function nativeNotificationActivation(value: unknown): NativeNotificationActivation {
  const id = stringProperty(value, "id");
  if (id === null || id.length === 0) {
    throw new Error("Native notification activation id must be a non-empty string.");
  }
  const target = nativeNotificationTarget(unknownProperty(value, "target"));
  return { id, target };
}

function nativeNotificationTarget(value: unknown): NativeNotificationTarget {
  const kind = stringProperty(value, "kind");
  if (kind !== "task_detail") {
    throw new Error("Native notification activation target kind must be task_detail.");
  }
  const taskID = stringProperty(value, "taskID");
  if (taskID === null || taskID.length === 0) {
    throw new Error("Native notification activation taskID must be a non-empty string.");
  }
  return {
    kind: "task_detail",
    taskID,
    focus: nativeNotificationFocus(unknownProperty(value, "focus")),
  };
}

function nativeNotificationFocus(
  value: unknown,
): NativeNotificationTarget["focus"] {
  const kind = stringProperty(value, "kind");
  if (kind === "question") {
    const askIDs = stringArrayProperty(value, "askIDs");
    const [firstAskID, ...remainingAskIDs] = askIDs;
    if (firstAskID === undefined) {
      throw new Error("Native notification question focus askIDs must contain at least one id.");
    }
    return { kind, askIDs: [firstAskID, ...remainingAskIDs] };
  }
  if (kind === "approval") {
    const taskTransitionID = stringProperty(value, "taskTransitionID");
    if (taskTransitionID === null || taskTransitionID.length === 0) {
      throw new Error("Native notification approval focus taskTransitionID must be a non-empty string.");
    }
    return { kind, taskTransitionID };
  }
  if (kind === "interrupted_run") {
    const runID = stringProperty(value, "runID");
    if (runID === null || runID.length === 0) {
      throw new Error("Native notification interrupted-run focus runID must be a non-empty string.");
    }
    return { kind, runID };
  }
  throw new Error("Native notification activation focus kind is unsupported.");
}

function stringProperty(value: unknown, property: string): string | null {
  const propertyValue = unknownProperty(value, property);
  return typeof propertyValue === "string" ? propertyValue : null;
}

function stringArrayProperty(value: unknown, property: string): string[] {
  const propertyValue = unknownProperty(value, property);
  if (!Array.isArray(propertyValue)) {
    return [];
  }
  const strings = propertyValue.filter((item): item is string => typeof item === "string" && item.length > 0);
  return strings.length === propertyValue.length ? strings : [];
}

function unknownProperty(value: unknown, property: string): unknown {
  return typeof value === "object" && value !== null ? Reflect.get(value, property) : undefined;
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
    notification:
      "Notification" in globalThis
        ? globalThis.Notification
        : null,
    focusWindow() {
      if (typeof window !== "undefined") {
        window.focus();
      }
    },
  };
}

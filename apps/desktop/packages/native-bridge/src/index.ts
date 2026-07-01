import { invoke } from "@tauri-apps/api/core";
import { emitTo, listen } from "@tauri-apps/api/event";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { readText, writeText } from "@tauri-apps/plugin-clipboard-manager";

import {
  fitCurrentWindowToContent,
  openNativeDialogWindow,
  type NativeDialogContentSize,
  type NativeDialogWindowOptions,
} from "./dialogs";
import {
  readBrowserDesktopSettings,
  readTauriDesktopSettings,
  writeBrowserDesktopSettings,
  writeTauriDesktopSettings,
  type DesktopSettings,
} from "./desktopSettings";
import {
  createBrowserCapabilities,
  createTauriCapabilities,
  normalizeNativePlatform,
  type NativeCapabilityState,
  type NativePlatform,
} from "./capabilities";
import {
  createBrowserNativeNotifications,
  createTauriNativeNotifications,
  type NativeNotificationBridge,
} from "./notifications";
import {
  createBrowserWindowFocusControls,
  createTauriWindowFocusControls,
} from "./windowFocus";
import {
  createBrowserUpdates,
  createTauriUpdates,
  type NativeUpdateBridge,
} from "./updates";

export type { NativeDialogContentSize, NativeDialogTheme, NativeDialogWindowOptions } from "./dialogs";
export {
  defaultDesktopSettings,
  desktopSettingsVersion,
  parseDesktopSettings,
  type DesktopSelfUpdate,
  type DesktopSettings,
} from "./desktopSettings";
export {
  createBrowserCapabilities,
  createTauriCapabilities,
  normalizeNativePlatform,
  type NativeCapabilityState,
  type NativePlatform,
} from "./capabilities";
export {
  NativeNotificationIDMapper,
  type NativeNotificationBackendID,
} from "./notificationIds";
export {
  createUnavailableNativeNotifications,
  createBrowserNativeNotifications,
  createTauriNativeNotifications,
  validateNativeNotification,
  type NativeNotification,
  type NativeNotificationActivation,
  type NativeNotificationApprovalFocus,
  type NativeNotificationPermission,
  type NativeNotificationQuestionFocus,
  type NativeNotificationTarget,
  type NativeNotificationTaskDetailTarget,
  type NativeNotificationBridge,
  type NativeNotificationUnlisten,
  type TauriNativeNotificationOptions,
} from "./notifications";
export {
  createBrowserWindowFocusControls,
  createTauriWindowFocusControls,
  type NativeWindowFocusControls,
} from "./windowFocus";
export {
  createBrowserUpdates,
  createTauriUpdates,
  type NativeUpdateAvailability,
  type NativeUpdateBridge,
  type NativeUpdateDownloadProgress,
} from "./updates";

export type NativeBridge = Readonly<{
  capabilities: NativeCapabilityState;
  clipboard: Readonly<{
    writeText(value: string): Promise<void>;
    readText(): Promise<string>;
  }>;
  directories: Readonly<{
    selectDirectory(options: NativeDirectoryPickerOptions): Promise<NativeDirectorySelection>;
  }>;
  notifications: NativeNotificationBridge;
  links: Readonly<{
    openExternal(url: string): Promise<void>;
  }>;
  logging: Readonly<{
    append(entry: NativeLogEntry): Promise<void>;
  }>;
  updates: NativeUpdateBridge;
  settings: Readonly<{
    read(): Promise<DesktopSettings>;
    write(next: DesktopSettings): Promise<void>;
  }>;
  app: Readonly<{
    resolvePlatform(): Promise<NativePlatform>;
    resolveContext(): Promise<NativeContext>;
  }>;
  window: Readonly<{
    startDragging(): Promise<void>;
    closeCurrent(): Promise<void>;
    isFocused(): Promise<boolean>;
    focusMain(): Promise<void>;
    onFocusChanged(handler: (focused: boolean) => void): Promise<NativeUnlisten>;
    fitCurrentToContent(size: NativeDialogContentSize): Promise<void>;
    setCurrentGlassTint(tint: NativeWindowGlassTint | null): Promise<void>;
  }>;
  dialogs: Readonly<{
    openWindow(options: NativeDialogWindowOptions): Promise<void>;
  }>;
  projectCreation: Readonly<{
    openWindow(draft: NativeProjectCreationDraft): Promise<void>;
    notifyCreated(binding: NativeProjectBinding): Promise<void>;
    onCreated(handler: (binding: NativeProjectBinding) => void): Promise<NativeUnlisten>;
  }>;
  projectDeletion: Readonly<{
    notifyDeleted(event: NativeProjectDeleted): Promise<void>;
    onDeleted(handler: (event: NativeProjectDeleted) => void): Promise<NativeUnlisten>;
  }>;
  projectWorkspace: Readonly<{
    requestUnlink(target: NativeWorkspaceUnlinkTarget): Promise<void>;
    onUnlinkRequested(handler: (target: NativeWorkspaceUnlinkTarget) => void): Promise<NativeUnlisten>;
    notifyChanged(event: NativeProjectWorkspaceChanged): Promise<void>;
    onChanged(handler: (event: NativeProjectWorkspaceChanged) => void): Promise<NativeUnlisten>;
  }>;
  workflowEditor: Readonly<{
    confirmGraphDelete(confirmation: NativeWorkflowGraphDeleteConfirmation): Promise<void>;
    onGraphDeleteConfirmed(
      handler: (confirmation: NativeWorkflowGraphDeleteConfirmation) => void,
    ): Promise<NativeUnlisten>;
  }>;
  workflowDeletion: Readonly<{
    notifyDeleted(event: NativeWorkflowDeleted): Promise<void>;
    onDeleted(handler: (event: NativeWorkflowDeleted) => void): Promise<NativeUnlisten>;
  }>;
}>;

export type NativeWindowGlassTint = Readonly<{
  red: number;
  green: number;
  blue: number;
  alpha: number;
}>;

const nativeWindowGlassTintChannels = ["red", "green", "blue", "alpha"] as const;

export type NativeDirectoryPickerOptions = Readonly<{
  title: string;
}>;

export type NativeDirectorySelection = Readonly<{
  path: string;
}> | null;

export type NativeLogEntry = Readonly<{
  level: "debug" | "info" | "warn" | "error";
  message: string;
  context: Readonly<Record<string, string>>;
  occurredAt: string;
}>;

export type NativeTheme = "auto" | "light" | "dark";

export type NativeContext = Readonly<{
  serverEndpoint: string;
  persistenceRoot: string;
  // persistenceRootId is the id a connected server must report
  // (HandshakeResponse.identity.persistence_root_id) for the GUI to trust it
  // serves this root. Empty when validation should be skipped (default root or
  // KENT_PERSISTENCE_ROOT unset).
  persistenceRootId: string;
  platform: NativePlatform;
  theme: NativeTheme;
  homePath: string;
}>;

export type NativeProjectCreationDraft = Readonly<{
  name: string;
  key: string;
  workspaceRoot: string;
}>;

export type NativeProjectBinding = Readonly<{
  projectID: string;
}>;

export type NativeProjectDeleted = Readonly<{
  projectID: string;
}>;

export type NativeWorkspaceUnlinkTarget = Readonly<{
  projectID: string;
  workspaceID: string;
  rootPath: string;
}>;

export type NativeProjectWorkspaceChanged = Readonly<{
  projectID: string;
}>;

export type NativeWorkflowGraphDeleteConfirmation = Readonly<{
  requestID: string;
}>;

export type NativeWorkflowDeleted = Readonly<{
  workflowID: string;
}>;

export type NativeUnlisten = () => void;

export const nativeDialogWindowHorizontalInsetPx = 16;
const projectDeletedEvent = "app://project-deleted";
const workspaceUnlinkRequestEvent = "app://workspace-unlink-request";
const projectWorkspaceChangedEvent = "app://project-workspace-changed";
const workflowGraphDeleteConfirmEvent = "app://workflow-graph-delete-confirm";
const workflowDeletedEvent = "app://workflow-deleted";

declare global {
  interface Window {
    __TAURI_INTERNALS__?: unknown;
  }
}

export type BrowserNativeBridgeOptions = Readonly<{
  platform?: NativePlatform | undefined;
}>;

export function createBrowserNativeBridge(options: BrowserNativeBridgeOptions = {}): NativeBridge {
  // Settings persist via localStorage so the browser QA shell (dev:browser) can
  // exercise settings-driven UI; the self-update gate never relies on this since
  // the browser shell is not updater-capable.
  const capabilities = createBrowserCapabilities(options.platform ?? "browser");
  const projectDeletionHandlers = new Set<(event: NativeProjectDeleted) => void>();
  const workflowDeletionHandlers = new Set<(event: NativeWorkflowDeleted) => void>();
  const browserWindowFocus = createBrowserWindowFocusControls();
  return {
    capabilities,
    clipboard: {
      async writeText(): Promise<void> {
        throw new Error("Native clipboard is unavailable in this shell.");
      },
      async readText(): Promise<string> {
        throw new Error("Native clipboard is unavailable in this shell.");
      },
    },
    directories: {
      async selectDirectory(): Promise<NativeDirectorySelection> {
        throw new Error("Directory selection is unavailable in this shell.");
      },
    },
    notifications: createBrowserNativeNotifications(),
    links: {
      async openExternal(url: string): Promise<void> {
        window.open(validateExternalUrl(url), "_blank", "noopener,noreferrer");
      },
    },
    logging: {
      async append(): Promise<void> {
        return Promise.resolve();
      },
    },
    updates: createBrowserUpdates(),
    settings: {
      async read(): Promise<DesktopSettings> {
        return readBrowserDesktopSettings();
      },
      async write(next: DesktopSettings): Promise<void> {
        writeBrowserDesktopSettings(next);
      },
    },
    app: {
      async resolvePlatform(): Promise<NativePlatform> {
        return capabilities.platform;
      },
      async resolveContext(): Promise<NativeContext> {
        return {
          serverEndpoint: "ws://127.0.0.1:53082/rpc",
          persistenceRoot: "",
          persistenceRootId: "",
          platform: capabilities.platform,
          theme: "auto",
          homePath: "",
        };
      },
    },
    window: {
      async startDragging(): Promise<void> {
        return Promise.resolve();
      },
      async closeCurrent(): Promise<void> {
        return Promise.resolve();
      },
      isFocused: browserWindowFocus.isFocused,
      focusMain: browserWindowFocus.focusMain,
      onFocusChanged: browserWindowFocus.onFocusChanged,
      async fitCurrentToContent(): Promise<void> {
        return Promise.resolve();
      },
      async setCurrentGlassTint(): Promise<void> {
        return Promise.resolve();
      },
    },
    dialogs: {
      async openWindow(): Promise<void> {
        throw new Error("Native dialog windows are unavailable in this shell.");
      },
    },
    projectCreation: {
      async openWindow(): Promise<void> {
        throw new Error("Native project creation window is unavailable in this shell.");
      },
      async notifyCreated(): Promise<void> {
        return Promise.resolve();
      },
      async onCreated(): Promise<NativeUnlisten> {
        return () => undefined;
      },
    },
    projectDeletion: {
      async notifyDeleted(event: NativeProjectDeleted): Promise<void> {
        await Promise.all(
          Array.from(projectDeletionHandlers, async (handler) => {
            handler(event);
          }),
        );
      },
      async onDeleted(handler: (event: NativeProjectDeleted) => void): Promise<NativeUnlisten> {
        projectDeletionHandlers.add(handler);
        return () => {
          projectDeletionHandlers.delete(handler);
        };
      },
    },
    projectWorkspace: {
      async requestUnlink(): Promise<void> {
        return Promise.resolve();
      },
      async onUnlinkRequested(): Promise<NativeUnlisten> {
        return () => undefined;
      },
      async notifyChanged(): Promise<void> {
        return Promise.resolve();
      },
      async onChanged(): Promise<NativeUnlisten> {
        return () => undefined;
      },
    },
    workflowEditor: {
      async confirmGraphDelete(): Promise<void> {
        return Promise.resolve();
      },
      async onGraphDeleteConfirmed(): Promise<NativeUnlisten> {
        return () => undefined;
      },
    },
    workflowDeletion: {
      async notifyDeleted(event: NativeWorkflowDeleted): Promise<void> {
        workflowDeletionHandlers.forEach((handler) => {
          handler(event);
        });
        return Promise.resolve();
      },
      async onDeleted(handler: (event: NativeWorkflowDeleted) => void): Promise<NativeUnlisten> {
        workflowDeletionHandlers.add(handler);
        return () => {
          workflowDeletionHandlers.delete(handler);
        };
      },
    },
  };
}

export function createTauriNativeBridge(platform: NativePlatform = "unknown"): NativeBridge {
  const capabilities = createTauriCapabilities(platform);
  const tauriWindowFocus = createTauriWindowFocusControls();
  return {
    capabilities,
    clipboard: {
      async writeText(value: string): Promise<void> {
        await writeText(value);
      },
      async readText(): Promise<string> {
        return readText();
      },
    },
    directories: {
      async selectDirectory(options: NativeDirectoryPickerOptions): Promise<NativeDirectorySelection> {
        const path = await invoke<string | null>("select_directory", { title: options.title });
        return path === null ? null : { path };
      },
    },
    notifications: createTauriNativeNotifications({ platform }),
    links: {
      async openExternal(url: string): Promise<void> {
        await invoke("open_external_url", { url: validateExternalUrl(url) });
      },
    },
    logging: {
      async append(entry: NativeLogEntry): Promise<void> {
        await invoke("append_gui_log", { entry: JSON.stringify(entry) });
      },
    },
    updates: createTauriUpdates(async () => invoke<boolean>("self_update_supported")),
    settings: {
      read: readTauriDesktopSettings,
      write: writeTauriDesktopSettings,
    },
    app: {
      async resolvePlatform(): Promise<NativePlatform> {
        return normalizeNativePlatform(await invoke<string>("resolve_native_platform"));
      },
      async resolveContext(): Promise<NativeContext> {
        return invoke<NativeContext>("resolve_native_context");
      },
    },
    window: {
      async startDragging(): Promise<void> {
        await getCurrentWindow().startDragging();
      },
      async closeCurrent(): Promise<void> {
        await getCurrentWindow().close();
      },
      isFocused: tauriWindowFocus.isFocused,
      focusMain: tauriWindowFocus.focusMain,
      onFocusChanged: tauriWindowFocus.onFocusChanged,
      async fitCurrentToContent(size: NativeDialogContentSize): Promise<void> {
        await fitCurrentWindowToContent(size);
      },
      async setCurrentGlassTint(tint: NativeWindowGlassTint | null): Promise<void> {
        validateNativeWindowGlassTint(tint);
        await invoke("set_native_window_glass_tint", {
          label: getCurrentWindow().label,
          tint,
        });
      },
    },
    dialogs: {
      async openWindow(options: NativeDialogWindowOptions): Promise<void> {
        await openNativeDialogWindow(options);
      },
    },
    projectCreation: {
      async openWindow(draft: NativeProjectCreationDraft): Promise<void> {
        await openNativeDialogWindow({
          initialHeight: 440,
          initialWidth: 640,
          label: `project-create-${Date.now().toString()}`,
          params: {
            key: draft.key,
            name: draft.name,
            workspaceRoot: draft.workspaceRoot,
          },
          route: "/native-dialog/project-create",
          title: "Create project",
        });
      },
      async notifyCreated(binding: NativeProjectBinding): Promise<void> {
        await emitTo("main", "app://project-created", binding);
      },
      async onCreated(handler: (binding: NativeProjectBinding) => void): Promise<NativeUnlisten> {
        return listen<NativeProjectBinding>("app://project-created", (event) => {
          handler(event.payload);
        });
      },
    },
    projectDeletion: {
      async notifyDeleted(event: NativeProjectDeleted): Promise<void> {
        await emitTo("main", projectDeletedEvent, event);
      },
      async onDeleted(handler: (event: NativeProjectDeleted) => void): Promise<NativeUnlisten> {
        return listen<NativeProjectDeleted>(projectDeletedEvent, (event) => {
          handler(event.payload);
        });
      },
    },
    projectWorkspace: {
      async requestUnlink(target: NativeWorkspaceUnlinkTarget): Promise<void> {
        await emitTo("main", workspaceUnlinkRequestEvent, target);
      },
      async onUnlinkRequested(
        handler: (target: NativeWorkspaceUnlinkTarget) => void,
      ): Promise<NativeUnlisten> {
        return listen<NativeWorkspaceUnlinkTarget>(workspaceUnlinkRequestEvent, (event) => {
          handler(event.payload);
        });
      },
      async notifyChanged(event: NativeProjectWorkspaceChanged): Promise<void> {
        await emitTo("main", projectWorkspaceChangedEvent, event);
      },
      async onChanged(handler: (event: NativeProjectWorkspaceChanged) => void): Promise<NativeUnlisten> {
        return listen<NativeProjectWorkspaceChanged>(projectWorkspaceChangedEvent, (event) => {
          handler(event.payload);
        });
      },
    },
    workflowEditor: {
      async confirmGraphDelete(confirmation: NativeWorkflowGraphDeleteConfirmation): Promise<void> {
        await emitTo("main", workflowGraphDeleteConfirmEvent, confirmation);
      },
      async onGraphDeleteConfirmed(
        handler: (confirmation: NativeWorkflowGraphDeleteConfirmation) => void,
      ): Promise<NativeUnlisten> {
        return listen<NativeWorkflowGraphDeleteConfirmation>(workflowGraphDeleteConfirmEvent, (event) => {
          handler(event.payload);
        });
      },
    },
    workflowDeletion: {
      async notifyDeleted(event: NativeWorkflowDeleted): Promise<void> {
        await emitTo("main", workflowDeletedEvent, event);
      },
      async onDeleted(handler: (event: NativeWorkflowDeleted) => void): Promise<NativeUnlisten> {
        return listen<NativeWorkflowDeleted>(workflowDeletedEvent, (event) => {
          handler(event.payload);
        });
      },
    },
  };
}

export function createAutoNativeBridge(platform: NativePlatform = "unknown"): NativeBridge {
  return isTauriRuntime()
    ? createTauriNativeBridge(platform)
    : createBrowserNativeBridge({ platform: "browser" });
}

function isTauriRuntime(): boolean {
  return typeof window !== "undefined" && window.__TAURI_INTERNALS__ !== undefined;
}

function validateExternalUrl(url: string): string {
  const parsed = new URL(url);
  if (!["http:", "https:", "mailto:"].includes(parsed.protocol)) {
    throw new Error("External link protocol is not allowed.");
  }
  return parsed.toString();
}

function validateNativeWindowGlassTint(tint: NativeWindowGlassTint | null): void {
  if (tint === null) {
    return;
  }
  for (const channel of nativeWindowGlassTintChannels) {
    const value = tint[channel];
    if (!Number.isFinite(value) || value < 0 || value > 1) {
      throw new Error(`Native glass tint ${channel} channel must be a finite number from 0 to 1.`);
    }
  }
}

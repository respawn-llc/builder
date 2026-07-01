import { invoke } from "@tauri-apps/api/core";
import { emitTo, listen } from "@tauri-apps/api/event";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { readText, writeText } from "@tauri-apps/plugin-clipboard-manager";
import { relaunch } from "@tauri-apps/plugin-process";
import { check as checkForUpdate, type Update } from "@tauri-apps/plugin-updater";

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

export type { NativeDialogContentSize, NativeDialogTheme, NativeDialogWindowOptions } from "./dialogs";
export {
  defaultDesktopSettings,
  desktopSettingsVersion,
  parseDesktopSettings,
  type DesktopSelfUpdate,
  type DesktopSettings,
} from "./desktopSettings";

export type NativeCapabilityState = Readonly<{
  platform: NativePlatform;
  clipboard: Readonly<{
    writeText: boolean;
    readText: boolean;
  }>;
  directories: Readonly<{
    select: boolean;
  }>;
  notifications: Readonly<{
    basic: boolean;
  }>;
  links: Readonly<{
    openExternal: boolean;
  }>;
  logging: Readonly<{
    localFile: boolean;
  }>;
  tray: boolean;
  appMenu: boolean;
  updater: boolean;
  settings: boolean;
  windowControls: boolean;
  windowDrag: boolean;
  dialogWindows: boolean;
  projectCreationWindow: boolean;
  macosVibrancy: boolean;
}>;

export type NativePlatform = "browser" | "linux" | "macos" | "unknown" | "windows";

export type NativeUpdateAvailability =
  | Readonly<{ available: false }>
  | Readonly<{
      available: true;
      version: string;
      currentVersion: string;
      notes: string | null;
      publishedAt: Date | null;
    }>;

export type NativeUpdateDownloadProgress = Readonly<{
  downloadedBytes: number;
  // Total bytes to download; null when the update server sent no Content-Length.
  totalBytes: number | null;
}>;

export type NativeBridge = Readonly<{
  capabilities: NativeCapabilityState;
  clipboard: Readonly<{
    writeText(value: string): Promise<void>;
    readText(): Promise<string>;
  }>;
  directories: Readonly<{
    selectDirectory(options: NativeDirectoryPickerOptions): Promise<NativeDirectorySelection>;
  }>;
  notifications: Readonly<{
    notify(message: NativeNotification): Promise<void>;
  }>;
  links: Readonly<{
    openExternal(url: string): Promise<void>;
  }>;
  logging: Readonly<{
    append(entry: NativeLogEntry): Promise<void>;
  }>;
  updates: Readonly<{
    // Reports whether the running install can self-update. The Tauri Linux updater
    // only services AppImage bundles, so deb/plain-binary launches return false
    // even though the updater capability is present.
    supported(): Promise<boolean>;
    check(): Promise<NativeUpdateAvailability>;
    downloadAndInstall(
      onProgress?: (progress: NativeUpdateDownloadProgress) => void,
    ): Promise<void>;
    relaunch(): Promise<void>;
  }>;
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

export type NativeNotification = Readonly<{
  title: string;
  body: string;
}>;

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

const unavailableCapabilities: NativeCapabilityState = {
  platform: "browser",
  clipboard: {
    writeText: false,
    readText: false,
  },
  directories: {
    select: false,
  },
  notifications: {
    basic: false,
  },
  links: {
    openExternal: false,
  },
  logging: {
    localFile: false,
  },
  tray: false,
  appMenu: false,
  updater: false,
  settings: false,
  windowControls: false,
  windowDrag: false,
  dialogWindows: false,
  projectCreationWindow: false,
  macosVibrancy: false,
};

const unavailableUpdate: NativeUpdateAvailability = { available: false };

function parseUpdateDate(value: string | undefined): Date | null {
  if (value === undefined) {
    return null;
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

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
  const capabilities = { ...unavailableCapabilities, platform: options.platform ?? "browser", settings: true };
  const projectDeletionHandlers = new Set<(event: NativeProjectDeleted) => void>();
  const workflowDeletionHandlers = new Set<(event: NativeWorkflowDeleted) => void>();
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
    notifications: {
      async notify(): Promise<void> {
        throw new Error("Native notifications are unavailable in this shell.");
      },
    },
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
    updates: {
      async supported(): Promise<boolean> {
        return false;
      },
      async check(): Promise<NativeUpdateAvailability> {
        return unavailableUpdate;
      },
      async downloadAndInstall(): Promise<void> {
        throw new Error("Application updates are unavailable in this shell.");
      },
      async relaunch(): Promise<void> {
        throw new Error("Application relaunch is unavailable in this shell.");
      },
    },
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
  // The Update handle returned by check() carries the connection used to download
  // and install; we hold it so downloadAndInstall() operates on the last check
  // result without leaking the plugin's Update type across the bridge boundary.
  let pendingUpdate: Update | null = null;
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
    notifications: {
      async notify(): Promise<void> {
        throw new Error("Native notifications are unavailable in this shell.");
      },
    },
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
    updates: {
      async supported(): Promise<boolean> {
        return invoke<boolean>("self_update_supported");
      },
      async check(): Promise<NativeUpdateAvailability> {
        const update = await checkForUpdate();
        pendingUpdate = update;
        if (update === null) {
          return unavailableUpdate;
        }
        return {
          available: true,
          version: update.version,
          currentVersion: update.currentVersion,
          notes: update.body ?? null,
          publishedAt: parseUpdateDate(update.date),
        };
      },
      async downloadAndInstall(
        onProgress?: (progress: NativeUpdateDownloadProgress) => void,
      ): Promise<void> {
        if (pendingUpdate === null) {
          throw new Error("No update is pending; call updates.check() first.");
        }
        let downloadedBytes = 0;
        let totalBytes: number | null = null;
        await pendingUpdate.downloadAndInstall((event) => {
          if (event.event === "Started") {
            totalBytes = event.data.contentLength ?? null;
            downloadedBytes = 0;
          } else if (event.event === "Progress") {
            downloadedBytes += event.data.chunkLength;
          }
          onProgress?.({ downloadedBytes, totalBytes });
        });
      },
      async relaunch(): Promise<void> {
        await relaunch();
      },
    },
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

function normalizeNativePlatform(platform: string): NativePlatform {
  if (platform === "linux" || platform === "macos" || platform === "windows") {
    return platform;
  }
  return "unknown";
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

function createTauriCapabilities(platform: NativePlatform): NativeCapabilityState {
  return {
    platform,
    clipboard: {
      writeText: true,
      readText: true,
    },
    directories: {
      select: true,
    },
    notifications: {
      basic: false,
    },
    links: {
      openExternal: true,
    },
    logging: {
      localFile: true,
    },
    tray: false,
    appMenu: false,
    updater: true,
    settings: true,
    windowControls: false,
    windowDrag: true,
    dialogWindows: true,
    projectCreationWindow: true,
    macosVibrancy: false,
  };
}

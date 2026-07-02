import {
  NativeNotificationIDMapper,
  createBrowserNativeNotifications,
  createBrowserNativeBridge,
  createBrowserCapabilities,
  createTauriNativeNotifications,
  createTauriNativeBridge,
  createTauriCapabilities,
  normalizeNativePlatform,
  type NativeNotificationPermission,
} from "@app/native-bridge";
import { vi } from "vitest";

import tauriDefaultCapability from "../src-tauri/capabilities/default.json";

type TestNotification = Readonly<{
  title: string;
  options: NotificationOptions | undefined;
  close: ReturnType<typeof vi.fn>;
  click(): void;
}>;

function notificationRuntime(initialPermission: NotificationPermission) {
  const created: TestNotification[] = [];
  const focusWindow = vi.fn();
  class NotificationStub {
    static permission: NotificationPermission = initialPermission;
    static requestPermission = vi.fn(async () => NotificationStub.permission);

    onclick: ((event: Event) => void) | null = null;
    readonly close = vi.fn();
    readonly title: string;
    readonly options: NotificationOptions | undefined;

    constructor(title: string, options?: NotificationOptions) {
      this.title = title;
      this.options = options;
      created.push({
        title,
        options,
        close: this.close,
        click: () => {
          this.onclick?.(new Event("click"));
        },
      });
    }
  }

  return {
    created,
    focusWindow,
    notification: NotificationStub,
  };
}

function tauriNotificationBackend(permissionGranted: boolean) {
  type SentNotification = Readonly<{
    backendId: number;
    id: string;
    title?: string;
    body?: string;
    target: {
      kind: "task_detail";
      taskID: string;
      focus:
        | { kind: "question"; askIDs: readonly [string, ...string[]] }
        | { kind: "approval"; taskTransitionID: string }
        | { kind: "interrupted_run"; runID: string };
    };
  }>;
  let actionHandler: ((activation: { id: string; target: SentNotification["target"] }) => void) | null =
    null;
  const sent: SentNotification[] = [];
  const removed: number[] = [];
  const backend = {
    async permissionState(): Promise<NativeNotificationPermission> {
      return permissionGranted ? "granted" : "denied";
    },
    async requestPermission(): Promise<NativeNotificationPermission> {
      return permissionGranted ? "granted" : "denied";
    },
    async send(options: SentNotification): Promise<void> {
      sent.push(options);
    },
    async onActivated(handler: (activation: { id: string; target: SentNotification["target"] }) => void): Promise<() => void> {
      actionHandler = handler;
      return () => {
        actionHandler = null;
      };
    },
    async removeActive(backendID: number): Promise<void> {
      removed.push(backendID);
    },
  };
  return {
    backend,
    removed,
    sent,
    triggerActivation(activation: { id: string; target: SentNotification["target"] }): void {
      actionHandler?.(activation);
    },
  };
}

function approvalNotificationFixture() {
  return {
    body: "Body",
    id: "k8_approvalu12_transition-1",
    target: {
      kind: "task_detail",
      taskID: "task-1",
      focus: { kind: "approval", taskTransitionID: "transition-1" },
    },
    title: "Title",
  } as const;
}

describe("native bridge capabilities", () => {
  it("keeps browser fallback capabilities disabled and explicit", async () => {
    const bridge = createBrowserNativeBridge();

    expect(bridge.capabilities.platform).toBe("browser");
    expect(bridge.capabilities.clipboard).toEqual({ readText: false, writeText: false });
    expect(bridge.capabilities.directories.select).toBe(false);
    expect(bridge.capabilities.files).toEqual({ open: false, select: false, stat: false });
    expect(bridge.capabilities.links.openExternal).toBe(false);
    expect(bridge.capabilities.notifications.basic).toBe(true);
    expect(bridge.capabilities.settings).toBe(true);
    expect(bridge.capabilities.dialogWindows).toBe(false);
    expect(bridge.capabilities.projectCreationWindow).toBe(false);
    await expect(bridge.app.resolvePlatform()).resolves.toBe("browser");
    await expect(createBrowserNativeBridge({ platform: "macos" }).app.resolvePlatform()).resolves.toBe(
      "macos",
    );
    await expect(bridge.clipboard.readText()).rejects.toThrow();
  });

  it("advertises Tauri capabilities only for implemented bridge methods", () => {
    const bridge = createTauriNativeBridge("macos");

    expect(bridge.capabilities.platform).toBe("macos");
    expect(bridge.capabilities.clipboard).toEqual({ readText: true, writeText: true });
    expect(bridge.capabilities.directories.select).toBe(true);
    expect(bridge.capabilities.files).toEqual({ open: true, select: true, stat: true });
    expect(bridge.capabilities.links.openExternal).toBe(true);
    expect(bridge.capabilities.logging.localFile).toBe(true);
    expect(bridge.capabilities.windowDrag).toBe(true);
    expect(bridge.capabilities.dialogWindows).toBe(true);
    expect(bridge.capabilities.projectCreationWindow).toBe(true);
    expect(bridge.capabilities.notifications.basic).toBe(true);
    expect(bridge.capabilities.tray).toBe(false);
    expect(bridge.capabilities.appMenu).toBe(false);
    expect(bridge.capabilities.updater).toBe(true);
    expect(bridge.capabilities.settings).toBe(true);
    expect(bridge.capabilities.macosVibrancy).toBe(false);
  });

  it("advertises native notification delivery for browser and desktop platforms", () => {
    expect(createBrowserNativeBridge().capabilities.notifications.basic).toBe(true);
    expect(createTauriNativeBridge("macos").capabilities.notifications.basic).toBe(true);
    expect(createTauriNativeBridge("windows").capabilities.notifications.basic).toBe(true);
    expect(createTauriNativeBridge("linux").capabilities.notifications.basic).toBe(true);
    expect(createTauriNativeBridge("unknown").capabilities.notifications.basic).toBe(false);
  });

  it("keeps capability decisions in the capability module", () => {
    expect(createBrowserCapabilities("browser").notifications.basic).toBe(true);
    expect(createBrowserCapabilities("macos").platform).toBe("macos");
    expect(createTauriCapabilities("macos").notifications.basic).toBe(true);
    expect(createTauriCapabilities("windows").notifications.basic).toBe(true);
    expect(createTauriCapabilities("linux").notifications.basic).toBe(true);
    expect(createTauriCapabilities("unknown").notifications.basic).toBe(false);
    expect(normalizeNativePlatform("macos")).toBe("macos");
    expect(normalizeNativePlatform("freebsd")).toBe("unknown");
  });

  it("uses browser system notifications with click activation", async () => {
    const runtime = notificationRuntime("granted");
    const bridge = createBrowserNativeNotifications(runtime);
    const handler = vi.fn();
    const notification = approvalNotificationFixture();

    await expect(bridge.permissionState()).resolves.toBe("granted");
    const unlisten = await bridge.onActivated(handler);
    await bridge.notify(notification);

    expect(runtime.created).toHaveLength(1);
    expect(runtime.created[0]?.title).toBe(notification.title);
    expect(runtime.created[0]?.options?.tag).toBe(notification.id);
    runtime.created[0]?.click();

    expect(runtime.focusWindow).toHaveBeenCalledOnce();
    expect(handler).toHaveBeenCalledWith({ id: notification.id, target: notification.target });
    unlisten();
  });

  it("removes active browser system notifications by Kent id", async () => {
    const runtime = notificationRuntime("granted");
    const bridge = createBrowserNativeNotifications(runtime);

    await bridge.notify({
      body: "Body",
      id: "k8_questionu7_batch-1",
      target: {
        kind: "task_detail",
        taskID: "task-1",
        focus: { kind: "question", askIDs: ["ask-1"] },
      },
      title: "Title",
    });
    await bridge.removeActive("k8_questionu7_batch-1");

    expect(runtime.created[0]?.close).toHaveBeenCalledOnce();
  });

  it("uses the Tauri desktop notification backend for delivery and activation", async () => {
    const tauri = tauriNotificationBackend(true);
    const bridge = createTauriNativeNotifications({ platform: "macos", backend: tauri.backend });
    const notification = approvalNotificationFixture();

    const handler = vi.fn();
    const unlisten = await bridge.onActivated(handler);
    await expect(bridge.permissionState()).resolves.toBe("granted");
    await bridge.notify(notification);
    const backendID = new NativeNotificationIDMapper().resolveBackendID(notification.id);

    expect(tauri.sent).toEqual([
      {
        backendId: backendID,
        ...notification,
      },
    ]);
    tauri.triggerActivation({ id: notification.id, target: notification.target });
    unlisten();
    await bridge.removeActive(notification.id);

    expect(handler).toHaveBeenCalledWith({ id: notification.id, target: notification.target });
    expect(tauri.removed).toEqual([backendID]);
  });

  it("fails Tauri native notification delivery before send when permission is not granted", async () => {
    const tauri = tauriNotificationBackend(false);
    const bridge = createTauriNativeNotifications({ platform: "macos", backend: tauri.backend });

    await expect(bridge.permissionState()).resolves.toBe("denied");
    await expect(
      bridge.notify({
        body: "Body",
        id: "k8_approvalu12_transition-1",
        target: {
          kind: "task_detail",
          taskID: "task-1",
          focus: { kind: "approval", taskTransitionID: "transition-1" },
        },
        title: "Title",
      }),
    ).rejects.toThrow();

    expect(tauri.sent).toEqual([]);
  });

  it("maps Kent notification ids to stable positive backend ids", () => {
    const mapper = new NativeNotificationIDMapper();

    const questionID = mapper.resolveBackendID("k8_questionu7_batch-1");
    const approvalID = mapper.resolveBackendID("k8_approvalu12_transition-1");

    expect(questionID).toBeGreaterThan(0);
    expect(approvalID).toBeGreaterThan(0);
    expect(mapper.resolveBackendID("k8_questionu7_batch-1")).toBe(questionID);
    expect(mapper.resolveStringID(questionID)).toBe("k8_questionu7_batch-1");
    expect(mapper.resolveStringID(approvalID)).toBe("k8_approvalu12_transition-1");
    expect(() => mapper.resolveBackendID("")).toThrow();
  });

  it("keeps Tauri permissions aligned with bridge event and window APIs", () => {
    const permissions = new Set(tauriDefaultCapability.permissions);

    expect(tauriDefaultCapability.windows).toContain("native-dialog-*");
    [
      "updater:default",
      "process:allow-restart",
      "store:default",
      "clipboard-manager:allow-read-text",
      "clipboard-manager:allow-write-text",
      "dialog:allow-open",
      "core:event:allow-emit",
      "notification:default",
      "core:event:allow-emit-to",
      "core:event:allow-listen",
      "core:event:allow-unlisten",
      "core:webview:allow-create-webview-window",
      "core:window:allow-close",
      "core:window:allow-get-all-windows",
      "core:window:allow-is-focused",
      "core:window:allow-set-focus",
      "core:window:allow-set-max-size",
      "core:window:allow-set-min-size",
      "core:window:allow-set-size",
      "core:window:allow-start-dragging",
    ].forEach((permission) => {
      expect(permissions.has(permission)).toBe(true);
    });
  });

  it("keeps browser workflow delete confirmation events as no-ops", async () => {
    const bridge = createBrowserNativeBridge();
    const handler = vi.fn();

    const unlisten = await bridge.workflowEditor.onGraphDeleteConfirmed(handler);
    await bridge.workflowEditor.confirmGraphDelete({ requestID: "delete-1" });
    unlisten();

    expect(handler).not.toHaveBeenCalled();
  });

  it("dispatches browser workflow deletion events for fallback dialogs", async () => {
    const bridge = createBrowserNativeBridge();
    const handler = vi.fn();

    const unlisten = await bridge.workflowDeletion.onDeleted(handler);
    await bridge.workflowDeletion.notifyDeleted({ workflowID: "workflow-1" });
    unlisten();
    await bridge.workflowDeletion.notifyDeleted({ workflowID: "workflow-2" });

    expect(handler).toHaveBeenCalledOnce();
    expect(handler).toHaveBeenCalledWith({ workflowID: "workflow-1" });
  });

  it("dispatches browser project deletion events for fallback dialogs", async () => {
    const bridge = createBrowserNativeBridge();
    const handler = vi.fn();

    const unlisten = await bridge.projectDeletion.onDeleted(handler);
    await bridge.projectDeletion.notifyDeleted({ projectID: "project-1" });
    unlisten();
    await bridge.projectDeletion.notifyDeleted({ projectID: "project-2" });

    expect(handler).toHaveBeenCalledOnce();
    expect(handler).toHaveBeenCalledWith({ projectID: "project-1" });
  });
});

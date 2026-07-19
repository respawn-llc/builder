import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeNotification,
  type NativeNotificationActivation,
  type NativeNotificationPermission,
} from "@app/native-bridge";
import { act, render, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";

import { App } from "./startup/App";
import { RpcError } from "@/api";
import { rpcErrorCodes } from "@/api";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { dismissStatusToast, showStatusToast, type StatusNotice } from "@/ui";
import type * as uiModule from "@/ui";
import { AppProviders } from "./AppProviders";
import { AttentionNotificationController } from "./AttentionNotificationController";
import { SidebarContext, type SidebarController } from "@/app-facade";

const statusToastHarness = vi.hoisted(() => ({
  notices: new Map<string, StatusNotice>(),
}));

vi.mock("@/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof uiModule>();
  return {
    ...actual,
    dismissStatusToast: vi.fn((id: string) => {
      const notice = statusToastHarness.notices.get(id);
      notice?.onDismiss?.();
      statusToastHarness.notices.delete(id);
    }),
    showStatusToast: vi.fn((notice: StatusNotice) => {
      statusToastHarness.notices.set(notice.id, notice);
    }),
    Toaster: () => null,
  };
});

type NativeBridgeHarness = Readonly<{
  bridge: NativeBridge;
  focusMain: ReturnType<typeof vi.fn>;
  notify: ReturnType<typeof vi.fn>;
  removeActive: ReturnType<typeof vi.fn>;
  triggerActivation(activation: NativeNotificationActivation): void;
}>;

type NativeBridgeHarnessOptions = Readonly<{
  focused: boolean | Promise<boolean>;
  nativeAvailable: boolean;
  notifyError?: Error | undefined;
  permission?: NativeNotificationPermission | undefined;
  requestPermissionError?: Error | undefined;
  requestedPermission?: NativeNotificationPermission | undefined;
}>;

describe("AttentionNotificationController", () => {
  beforeEach(() => {
    statusToastHarness.notices.clear();
    vi.mocked(dismissStatusToast).mockClear();
    vi.mocked(showStatusToast).mockClear();
  });

  it("shows one persistent Sonner while focused and dismisses it on resolved", async () => {
    const { emit, native } = await attentionFixture({ focused: true, nativeAvailable: true });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));

    await waitForSonnerArticleCount(1);
    expect(native.notify).not.toHaveBeenCalled();

    emit(attentionPendingEvent({ revision: 2, sequence: 2 }));
    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(1);
    });

    emit(attentionResolvedEvent({ sequence: 3 }));
    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });
    await waitFor(() => {
      expect(native.removeActive).toHaveBeenCalledWith("k8_questionu7_batch-1");
    });
  });

  it("sends one native notification while unfocused", async () => {
    const { emit, native } = await attentionFixture({ focused: false, nativeAvailable: true });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));

    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledOnce();
    });
    expect(native.notify).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "k8_questionu7_batch-1",
        target: {
          kind: "task_detail",
          taskID: "task-1",
          focus: { kind: "question", askIDs: ["ask-1", "ask-2"] },
        },
      }),
    );
    expect(sonnerArticles()).toHaveLength(0);

    emit(attentionPendingEvent({ revision: 2, sequence: 2 }));
    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledTimes(2);
    });
  });

  it("ignores unsupported root attention targets at the desktop boundary", async () => {
    const { emit, native } = await attentionFixture({ focused: true, nativeAvailable: true });
    emit(attentionSessionPromptPendingEvent({ sequence: 1 }));

    await waitFor(() => {
      expect(native.notify).not.toHaveBeenCalled();
    });
    expect(sonnerArticles()).toHaveLength(0);
  });

  it("coalesces pending updates when an update races focus resolution", async () => {
    const focus = deferred<boolean>();
    const { emit, native } = await attentionFixture({
      focused: focus.promise,
      nativeAvailable: false,
    });
    emit(
      attentionPendingEvent({ revision: 1, sequence: 1, body: "Old question text" }),
      attentionPendingEvent({ revision: 2, sequence: 2, body: "Latest question text" }),
    );
    await act(async () => {
      focus.resolve(true);
      await focus.promise;
    });

    await waitForSonnerArticleCount(1);
    expect(native.notify).not.toHaveBeenCalled();
  });

  it("logs and stops surfacing when native delivery fails", async () => {
    const { emit, native } = await attentionFixture({
      focused: false,
      nativeAvailable: true,
      notifyError: new Error("notification send failed"),
    });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));

    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledOnce();
    });
    expect(sonnerArticles()).toHaveLength(0);
  });

  it("sends interrupted-run native notifications with run focus", async () => {
    const { emit, native } = await attentionFixture({ focused: false, nativeAvailable: true });
    emit(attentionInterruptedRunPendingEvent({ sequence: 1 }));

    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledOnce();
    });
    expect(native.notify).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "k15_interrupted_runu5_run-1",
        target: {
          kind: "task_detail",
          taskID: "task-1",
          focus: { kind: "interrupted_run", runID: "run-1" },
        },
      }),
    );
  });

  it("does not surface stale pending attention after resolved races native delivery", async () => {
    const { emit, native } = await attentionFixture({ focused: false, nativeAvailable: true });
    let rejectDelivery: ((error: Error) => void) | null = null;
    const delivery = new Promise<void>((_, reject) => {
      rejectDelivery = reject;
    });
    native.notify.mockReturnValueOnce(delivery);

    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));
    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledOnce();
    });

    emit(attentionResolvedEvent({ sequence: 2 }));
    await waitFor(() => {
      expect(native.removeActive).toHaveBeenCalledWith("k8_questionu7_batch-1");
    });
    await act(async () => {
      rejectDelivery?.(new Error("notification send failed"));
      await Promise.resolve();
    });

    expect(sonnerArticles()).toHaveLength(0);
  });

  it("opens task detail from Sonner action and native activation", async () => {
    const { emit, native, services } = await attentionFixture({ focused: true, nativeAvailable: true });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));

    await waitForSonnerArticleCount(1);
    expect(singleSonnerArticle().onAction).toBeUndefined();
    clickFirstSonnerButton();
    await waitFor(() => {
      expect(native.focusMain).toHaveBeenCalledOnce();
      expect(screen.getByTestId("app-sidebar-host")).toBeInTheDocument();
    });
    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });

    emit(attentionPendingEvent({ revision: 2, sequence: 2 }));
    await waitFor(() => {
      expect(native.focusMain).toHaveBeenCalledOnce();
      expect(sonnerArticles()).toHaveLength(0);
    });

    act(() => {
      native.triggerActivation({
        id: "k8_approvalu12_transition-1",
        target: {
          kind: "task_detail",
          taskID: "task-2",
          focus: { kind: "approval", taskTransitionID: "transition-1" },
        },
      });
    });

    await waitFor(() => {
      expect(native.focusMain).toHaveBeenCalledTimes(2);
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.get",
        params: { task_id: "task-2" },
      });
    });
  });

  it("surfaces a later pending revision after activated toast opening fails", async () => {
    const native = nativeBridgeHarness({ focused: true, nativeAvailable: true });
    const services = createTestServices(startupRoutes, native.bridge);
    const openSidebar = vi.fn(async () => {
      throw new Error("open failed");
    });

    render(
      <AppProviders services={services}>
        <SidebarContext.Provider value={sidebarController({ openSidebar })}>
          <AttentionNotificationController />
        </SidebarContext.Provider>
      </AppProviders>,
    );
    await waitForAttentionSubscription(services.transport);
    act(() => {
      services.transport.emit("attention.notification", attentionPendingEvent({ revision: 1, sequence: 1 }));
    });
    await waitForSonnerArticleCount(1);

    clickFirstSonnerButton();
    await waitFor(() => {
      expect(openSidebar).toHaveBeenCalledOnce();
    });
    await waitForSonnerArticleCount(0);

    act(() => {
      services.transport.emit("attention.notification", attentionPendingEvent({ revision: 2, sequence: 2 }));
    });

    await waitForSonnerArticleCount(1);
  });

  it("does not recreate a manually dismissed Sonner for the same pending notification", async () => {
    const { emit, native } = await attentionFixture({ focused: true, nativeAvailable: true });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));
    await waitForSonnerArticleCount(1);

    dismissStatusToast(singleSonnerArticle().id);
    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });

    emit(attentionPendingEvent({ revision: 2, sequence: 2 }));
    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });
    expect(native.notify).not.toHaveBeenCalled();
  });

  it("shows one startup warning when native notification permission is denied", async () => {
    const native = nativeBridgeHarness({
      focused: true,
      nativeAvailable: true,
      permission: "prompt",
      requestedPermission: "denied",
    });
    const services = createTestServices(startupRoutes, native.bridge);

    render(<App services={services} />);

    await waitForSonnerArticleCount(1);
  });

  it("logs native notification permission request failures", async () => {
    const native = nativeBridgeHarness({
      focused: true,
      nativeAvailable: true,
      permission: "prompt",
      requestPermissionError: new Error("No bundle identifier found."),
    });
    const services = createTestServices(startupRoutes, native.bridge);

    render(<App services={services} />);

    await waitFor(() => {
      expect(services.logger.entries()).toContainEqual(
        expect.objectContaining({
          level: "warn",
          context: {
            error: "No bundle identifier found.",
          },
        }),
      );
    });
  });

  it("reconciles surfaced attention after reconnect without notifying durable baseline attention", async () => {
    const { emit, native, reconnect, services } = await attentionFixture({
      focused: true,
      nativeAvailable: false,
      routes: [
        ...startupRoutes,
        {
          method: "workflow.task.attention.list",
          result: taskAttentionResult([]),
        },
      ],
    });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));
    await waitForSonnerArticleCount(1);

    reconnect();

    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });
    expect(native.notify).not.toHaveBeenCalled();
    expect(services.transport.calls).toContainEqual({
      method: "workflow.task.attention.list",
      params: { task_id: "task-1" },
    });
  });

  it("removes native notifications when reconnect reconciliation finds stale attention", async () => {
    const { emit, native, reconnect } = await attentionFixture({
      focused: false,
      nativeAvailable: true,
      routes: [
        ...startupRoutes,
        {
          method: "workflow.task.attention.list",
          result: taskAttentionResult([]),
        },
      ],
    });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));
    await waitFor(() => {
      expect(native.notify).toHaveBeenCalledOnce();
    });

    reconnect();

    await waitFor(() => {
      expect(native.removeActive).toHaveBeenCalledWith("k8_questionu7_batch-1");
    });
  });

  it.each([
    {
      reason: "structured task-not-found errors",
      taskRoute: {
        method: "workflow.task.attention.list",
        error: new RpcError({
          code: rpcErrorCodes.workflowTaskNotFound,
          message: "not localized",
          method: "workflow.task.attention.list",
        }),
      },
    },
    {
      reason: "a reused ask id from another run",
      taskRoute: {
        method: "workflow.task.attention.list",
        result: taskAttentionResult([
          durableAttentionItem({
            askID: "ask-1",
            runID: "run-2",
            sessionID: "session-1",
          }),
        ]),
      },
    },
  ])("treats $reason as stale during reconnect reconciliation", async ({ taskRoute }) => {
    const { emit, reconnect } = await attentionFixture({
      focused: true,
      nativeAvailable: false,
      routes: [...startupRoutes, taskRoute],
    });
    emit(attentionPendingEvent({ revision: 1, sequence: 1 }));
    await waitForSonnerArticleCount(1);

    reconnect();

    await waitFor(() => {
      expect(sonnerArticles()).toHaveLength(0);
    });
  });

  it("does not notify durable baseline attention loaded during startup", async () => {
    const { native, services } = await attentionFixture({
      focused: false,
      nativeAvailable: true,
      routes: [
        ...startupRoutes,
        {
          method: "workflow.attention.list",
          result: {
            items: [durableAttentionItem()],
            next_page_token: "",
            generated_at_unix_ms: 1,
          },
        },
      ],
    });

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual(
        expect.objectContaining({ method: "workflow.attention.list" }),
      );
    });
    expect(native.notify).not.toHaveBeenCalled();
    expect(sonnerArticles()).toHaveLength(0);
  });
});

function sonnerArticles(): StatusNotice[] {
  return [...statusToastHarness.notices.values()];
}

async function waitForSonnerArticleCount(count: number): Promise<void> {
  await waitFor(() => {
    expect(sonnerArticles()).toHaveLength(count);
  });
}

function singleSonnerArticle(): StatusNotice {
  const [notice] = sonnerArticles();
  if (notice === undefined) {
    throw new Error("Expected a surfaced status toast.");
  }
  return notice;
}

function clickFirstSonnerButton(): void {
  const notice = singleSonnerArticle();
  if (notice.onClick === undefined) {
    throw new Error("Expected a clickable status toast.");
  }
  notice.onClick();
}

function sidebarController({
  openSidebar,
}: Readonly<{ openSidebar: SidebarController["openSidebar"] }>): SidebarController {
  return {
    activeDestination: null,
    closeSidebar: vi.fn(),
    openSidebar,
    phase: "open",
    resizeSidebar: vi.fn(),
    resolveSidebar: vi.fn(),
    sidebarWidthPx: 0,
  };
}

async function attentionFixture({
  routes = startupRoutes,
  ...nativeOptions
}: NativeBridgeHarnessOptions &
  Readonly<{
    routes?: Parameters<typeof createTestServices>[0] | undefined;
  }>) {
  const native = nativeBridgeHarness(nativeOptions);
  const services = createTestServices(routes, native.bridge);
  render(<App services={services} />);
  await waitForAttentionSubscription(services.transport);
  return {
    native,
    services,
    emit: (...events: readonly unknown[]): void => {
      act(() => {
        for (const event of events) {
          services.transport.emit("attention.notification", event);
        }
      });
    },
    reconnect: (): void => {
      act(() => {
        services.transport.connection.set("disconnected", "stream gap");
        services.transport.connection.set("connected");
      });
    },
  };
}

function nativeBridgeHarness({
  focused,
  nativeAvailable,
  notifyError,
  permission = "granted",
  requestPermissionError,
  requestedPermission,
}: NativeBridgeHarnessOptions): NativeBridgeHarness {
  const base = createBrowserNativeBridge({ platform: "macos" });
  let activationHandler: ((activation: NativeNotificationActivation) => void) | null = null;
  const focusMain = vi.fn(async () => undefined);
  const notify = vi.fn(async (message: NativeNotification) => {
    void message;
    if (notifyError !== undefined) {
      throw notifyError;
    }
  });
  const removeActive = vi.fn(async () => undefined);
  return {
    bridge: {
      ...base,
      capabilities: {
        ...base.capabilities,
        notifications: { basic: nativeAvailable },
        platform: "macos",
      },
      notifications: {
        ...base.notifications,
        async permissionState(): Promise<NativeNotificationPermission> {
          return permission;
        },
        async requestPermission(): Promise<NativeNotificationPermission> {
          if (requestPermissionError !== undefined) {
            throw requestPermissionError;
          }
          return requestedPermission ?? permission;
        },
        notify,
        removeActive,
        async onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<() => void> {
          activationHandler = handler;
          return () => {
            activationHandler = null;
          };
        },
      },
      window: {
        ...base.window,
        focusMain,
        async isFocused(): Promise<boolean> {
          return focused;
        },
        async onFocusChanged(): Promise<() => void> {
          return () => undefined;
        },
      },
    },
    focusMain,
    notify,
    removeActive,
    triggerActivation(activation: NativeNotificationActivation): void {
      activationHandler?.(activation);
    },
  };
}

async function waitForAttentionSubscription(transport: { subscriptions: readonly { method: string }[] }) {
  return waitFor(() => {
    expect(
      transport.subscriptions.some(
        (subscription) => subscription.method === "attention.notification.subscribe",
      ),
    ).toBe(true);
  });
}

function attentionPendingEvent({
  body = "Pick a deployment target",
  revision,
  sequence,
}: Readonly<{ body?: string | undefined; revision: number; sequence: number }>): unknown {
  return {
    event: {
      type: "pending",
      sequence,
      source: "live",
      pending: {
        id: { kind: "question", uuid: "batch-1" },
        kind: "question",
        occurred_at: "2026-06-29T12:00:00Z",
        revision,
        question: {
          prepared_ask_ids: ["ask-1", "ask-2"],
          materialized_ask_ids: ["ask-1"],
          current_unresolved_ask_ids: ["ask-1"],
          skipped_ask_ids: [],
          preview: body,
          display_count: 2,
          materialized_count: 1,
        },
        target: {
          kind: "workflow_task",
          project_id: "project-1",
          workflow_id: "workflow-1",
          task_id: "task-1",
          task_short_id: "KT-1",
          task_title: "Needs answer",
          session_id: "session-1",
          run_id: "run-1",
          focus: { kind: "question", ask_ids: ["ask-1", "ask-2"] },
        },
      },
    },
  };
}

function attentionInterruptedRunPendingEvent({ sequence }: Readonly<{ sequence: number }>): unknown {
  return {
    event: {
      type: "pending",
      sequence,
      source: "live",
      pending: {
        id: { kind: "interrupted_run", uuid: "run-1" },
        kind: "interrupted_run",
        occurred_at: "2026-06-29T12:00:00Z",
        revision: 1,
        interrupted_run: {
          run_id: "run-1",
          message: "Run interrupted",
          reason: "workflow_runtime_failed",
        },
        target: {
          kind: "workflow_task",
          project_id: "project-1",
          workflow_id: "workflow-1",
          task_id: "task-1",
          task_short_id: "KT-1",
          task_title: "Needs recovery",
          session_id: "session-1",
          run_id: "run-1",
          focus: { kind: "interrupted_run", run_id: "run-1" },
        },
      },
    },
  };
}

function attentionSessionPromptPendingEvent({ sequence }: Readonly<{ sequence: number }>): unknown {
  return {
    event: {
      type: "pending",
      sequence,
      source: "live",
      pending: {
        id: { kind: "question", uuid: "ask-1" },
        kind: "question",
        occurred_at: "2026-06-29T12:00:00Z",
        revision: 1,
        target: {
          kind: "session_prompt",
          session_id: "session-1",
        },
      },
    },
  };
}

function attentionResolvedEvent({ sequence }: Readonly<{ sequence: number }>): unknown {
  return {
    event: {
      type: "resolved",
      sequence,
      source: "live",
      id: { kind: "question", uuid: "batch-1" },
      kind: "question",
      occurred_at: "2026-06-29T12:05:00Z",
    },
  };
}

function durableAttentionItem({
  askID = "ask-old",
  runID = "run-1",
  sessionID = "session-1",
}: Readonly<{
  askID?: string | undefined;
  runID?: string | undefined;
  sessionID?: string | undefined;
}> = {}): unknown {
  return {
    id: "attention-old",
    kind: "question",
    project_id: "project-1",
    workflow_id: "workflow-1",
    task_id: "task-1",
    task_short_id: "KT-1",
    task_title: "Needs answer",
    run_id: runID,
    session_id: sessionID,
    ask_id: askID,
    task_transition_id: "",
    message: "Old pending question",
    suggestions: [],
    recommended_option_index: 0,
    occurred_at_unix_ms: 1,
  };
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}

function taskAttentionResult(attention: readonly unknown[]): unknown {
  return {
    items: attention,
    generated_at_unix_ms: 1,
  };
}

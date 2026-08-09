import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { createBrowserNativeBridge, type NativeNotificationActivation } from "@app/native-bridge";

import { parseSetupOperationID, type AttentionNotification } from "@/api";
import {
  AppServicesProvider,
  SidebarRootContext,
  StatusContext,
  type SidebarDestination,
} from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { createTestSidebarController } from "@/test-support/sidebar";
import {
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponseWithInterruptedCurrentScript,
} from "@/test-support/task-detail";
import type { StatusNotice } from "@/ui";
import { AttentionNotificationController } from "./AttentionNotificationController";
import { attentionNotificationSurfaceKey, attentionToastID } from "./attentionNotificationSurfaces";

it("surfaces one persistent canonical setup notification while no Task route is mounted", async () => {
  const baseBridge = createBrowserNativeBridge();
  const focusMain = vi.fn(async () => undefined);
  const services = createTestServices([], {
    ...baseBridge,
    window: {
      ...baseBridge.window,
      focusMain,
      async isFocused() {
        return true;
      },
    },
  });
  const notices: StatusNotice[] = [];
  const destinations: SidebarDestination[] = [];
  const status = {
    dismiss: vi.fn(),
    push: vi.fn((notice: StatusNotice) => {
      notices.push(notice);
    }),
  };
  const setupOperationID = "88888888-8888-4888-8888-888888888888";

  render(
    <AppServicesProvider services={services}>
      <StatusContext.Provider value={status}>
        <SidebarRootContext.Provider
          value={createTestSidebarController((destination) => {
            destinations.push(destination);
          })}
        >
          <AttentionNotificationController />
        </SidebarRootContext.Provider>
      </StatusContext.Provider>
    </AppServicesProvider>,
  );
  await waitFor(() => {
    expect(services.transport.subscriptions).toContainEqual({
      method: "attention.notification.subscribe",
      params: {},
    });
  });
  const pending = setupRecoveryNotification(setupOperationID);
  const pendingToastID = setupRecoveryToastID(setupOperationID);

  act(() => {
    services.transport.emit("attention.notification", {
      event: { type: "pending", sequence: 1, source: "live", pending },
    });
  });
  await waitFor(() => {
    expect(attentionNotices(notices, [pendingToastID])).toHaveLength(1);
  });
  const notice = attentionNotices(notices, [pendingToastID])[0];
  expect(notice?.durationMs).toBe(Infinity);

  act(() => {
    services.transport.emit("attention.notification", {
      event: { type: "pending", sequence: 2, source: "snapshot", pending },
    });
  });
  await waitFor(() => {
    expect(attentionNotices(notices, [pendingToastID])).toHaveLength(1);
  });

  const nextSetupOperationID = "99999999-9999-4999-8999-999999999999";
  const nextPending = setupRecoveryNotification(nextSetupOperationID);
  const nextToastID = setupRecoveryToastID(nextSetupOperationID);
  act(() => {
    services.transport.emit("attention.notification", {
      event: {
        type: "pending",
        sequence: 3,
        source: "live",
        pending: nextPending,
      },
    });
  });
  await waitFor(() => {
    expect(attentionNotices(notices, [pendingToastID, nextToastID])).toHaveLength(2);
  });
  const nextNotice = attentionNotices(notices, [nextToastID])[0];

  await act(async () => {
    nextNotice?.onClick?.();
  });
  await waitFor(() => {
    expect(destinations).toHaveLength(1);
  });
  expect(focusMain).toHaveBeenCalledOnce();
  expect(destinations[0]).toMatchObject({
    kind: "taskDetail",
    taskID: "task-1",
    initialFocus: {
      kind: "interrupted_current_node",
      currentNodeID: "canonical-node",
      currentNodeBranchKey: "branch-2",
    },
  });
  const destination = destinations[0];
  if (destination?.kind !== "taskDetail" || destination.initialFocus?.kind !== "interrupted_current_node") {
    throw new Error("Expected exact setup-recovery Task-detail focus.");
  }
  expect(destination.initialFocus.setupOperationID?.toJSONValue()).toBe(nextSetupOperationID);
});

it("maps native setup-notification activation to exact Task-detail focus", async () => {
  const baseBridge = createBrowserNativeBridge();
  let activationHandler: ((activation: NativeNotificationActivation) => void) | undefined;
  const focusMain = vi.fn(async () => undefined);
  const services = createTestServices([], {
    ...baseBridge,
    notifications: {
      ...baseBridge.notifications,
      async onActivated(handler) {
        activationHandler = handler;
        return () => undefined;
      },
    },
    window: {
      ...baseBridge.window,
      focusMain,
    },
  });
  const destinations: SidebarDestination[] = [];
  render(
    <AppServicesProvider services={services}>
      <StatusContext.Provider value={{ dismiss: vi.fn(), push: vi.fn() }}>
        <SidebarRootContext.Provider
          value={createTestSidebarController((destination) => {
            destinations.push(destination);
          })}
        >
          <AttentionNotificationController />
        </SidebarRootContext.Provider>
      </StatusContext.Provider>
    </AppServicesProvider>,
  );
  await waitFor(() => {
    expect(activationHandler).toBeDefined();
  });
  const setupOperationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

  act(() => {
    activationHandler?.({
      id: "native-setup-recovery",
      target: {
        kind: "task_detail",
        taskID: "task-1",
        focus: {
          kind: "interrupted_current_node",
          currentNodeID: "canonical-node",
          currentNodeBranchKey: "branch-2",
          setupOperationID,
        },
      },
    });
  });
  await waitFor(() => {
    expect(destinations).toHaveLength(1);
  });
  expect(focusMain).toHaveBeenCalledOnce();
  const destination = destinations[0];
  if (destination?.kind !== "taskDetail" || destination.initialFocus?.kind !== "interrupted_current_node") {
    throw new Error("Expected exact native setup-recovery Task-detail focus.");
  }
  expect(destination.initialFocus).toMatchObject({
    currentNodeID: "canonical-node",
    currentNodeBranchKey: "branch-2",
  });
  expect(destination.initialFocus.setupOperationID?.toJSONValue()).toBe(setupOperationID);
});

it.each(["live", "snapshot"] as const)(
  "continues %s canonical setup attention through Task Detail without setup observation",
  async (source) => {
    Element.prototype.scrollIntoView = vi.fn();
    const baseBridge = createBrowserNativeBridge();
    const services = createTestServices([], {
      ...baseBridge,
      window: {
        ...baseBridge.window,
        async isFocused() {
          return true;
        },
      },
    });
    const notices: StatusNotice[] = [];
    const destinations: SidebarDestination[] = [];
    const setupOperationID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
    const pending = setupRecoveryNotification(setupOperationID);
    const pendingToastID = setupRecoveryToastID(setupOperationID);
    const view = render(
      <AppServicesProvider services={services}>
        <StatusContext.Provider
          value={{
            dismiss: vi.fn(),
            push: vi.fn((notice: StatusNotice) => {
              notices.push(notice);
            }),
          }}
        >
          <SidebarRootContext.Provider
            value={createTestSidebarController((destination) => {
              destinations.push(destination);
            })}
          >
            <AttentionNotificationController />
          </SidebarRootContext.Provider>
        </StatusContext.Provider>
      </AppServicesProvider>,
    );
    await waitFor(() => {
      expect(services.transport.subscriptions).toContainEqual({
        method: "attention.notification.subscribe",
        params: {},
      });
    });

    act(() => {
      services.transport.emit("attention.notification", {
        event: {
          type: "pending",
          sequence: 1,
          source,
          pending,
        },
      });
    });
    await waitFor(() => {
      expect(attentionNotices(notices, [pendingToastID])).toHaveLength(1);
    });
    await act(async () => {
      attentionNotices(notices, [pendingToastID])[0]?.onClick?.();
    });
    const destination = destinations[0];
    if (destination?.kind !== "taskDetail") {
      throw new Error("Expected live setup attention to navigate to Task Detail.");
    }
    view.unmount();

    const taskServices = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
      initialFocus: destination.initialFocus,
      attention: setupRecoveryTaskAttention(setupOperationID),
      routes: [
        {
          method: "workflow.task.resume",
          result: {
            outcome: "applied",
            applied: {
              current_nodes: [
                {
                  node_id: "canonical-node",
                  transition_branch_key: "branch-2",
                  session_id: null,
                },
              ],
            },
          },
        },
      ],
    });
    const user = userEvent.setup();
    const resumeButtons = await screen.findAllByTestId("task-detail-resume");
    expect(resumeButtons).toHaveLength(1);
    const resumeButton = resumeButtons[0];
    if (resumeButton === undefined) {
      throw new Error("Expected the canonical Resume button.");
    }
    await user.click(resumeButton);
    await user.click(await screen.findByTestId("setup-recovery-retry"));
    await waitFor(() => {
      expect(getCallCount(taskServices.transport.calls, "workflow.task.resume")).toBe(1);
    });
    expect(
      taskServices.transport.subscriptionStarts.some(
        (subscription) => subscription.method === "worktree.setup.subscribe",
      ),
    ).toBe(false);
  },
);

function setupRecoveryNotification(setupOperationID: string) {
  return {
    id: { kind: "interrupted_current_node", uuid: "canonical-attention" },
    kind: "interrupted_current_node",
    occurred_at: "2026-08-08T12:00:00Z",
    revision: 4,
    interrupted_current_node: {
      message: "Setup needs attention",
      reason: "workflow_setup_recovery",
    },
    target: {
      kind: "workflow_task",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      task_id: "task-1",
      current_node_id: "canonical-node",
      current_node_branch_key: "branch-2",
      focus: {
        kind: "interrupted_current_node",
        setup_operation_id: setupOperationID,
      },
    },
  } as const;
}

function setupRecoveryTaskAttention(setupOperationID: string) {
  return {
    items: [
      {
        id: "generic-sibling",
        kind: "interrupted_current_node",
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        task_id: "task-1",
        task_short_id: "T-1",
        task_title: "Resolve blocker",
        current_node: {
          node_id: "sibling-node",
          transition_branch_key: null,
          session_id: null,
        },
        session_id: null,
        session_name: null,
        setup_operation_id: null,
        detail_json: JSON.stringify({ code: "workflow_runtime_failed", fields: {} }),
        occurred_at_unix_ms: 1,
      },
      {
        id: "canonical-recovery",
        kind: "interrupted_current_node",
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        task_id: "task-1",
        task_short_id: "T-1",
        task_title: "Resolve blocker",
        current_node: {
          node_id: "canonical-node",
          transition_branch_key: "branch-2",
          session_id: null,
        },
        session_id: null,
        session_name: null,
        setup_operation_id: setupOperationID,
        detail_json: JSON.stringify({
          code: "workflow_setup_recovery",
          fields: {},
          setup_recovery: {
            setup_operation_id: setupOperationID,
            cause: "target_preparation",
            diagnostic: "target preparation failed",
            script_path: null,
            setup_requirement: "required",
            execution_target: { mode: "default_branch" },
            retained_worktree: null,
            retained_previous_worktree: null,
          },
        }),
        occurred_at_unix_ms: 2,
      },
    ],
    generated_at_unix_ms: 3,
  };
}

function attentionNotices(
  notices: readonly StatusNotice[],
  expectedIDs: readonly string[],
): readonly StatusNotice[] {
  const expected = new Set(expectedIDs);
  return notices.filter((notice) => expected.has(notice.id));
}

function setupRecoveryToastID(setupOperationID: string): string {
  const notification: AttentionNotification = {
    id: { kind: "interrupted_current_node", uuid: "canonical-attention" },
    kind: "interrupted_current_node",
    occurredAt: "2026-08-08T12:00:00Z",
    revision: 4,
    question: null,
    approval: null,
    workflowApproval: null,
    interruptedCurrentNode: {
      message: "Setup needs attention",
      reason: "workflow_setup_recovery",
    },
    target: {
      kind: "workflow_task",
      workflowID: "11111111-1111-4111-8111-111111111111",
      taskID: "task-1",
      currentNodeID: "canonical-node",
      currentNodeBranchKey: "branch-2",
      focus: {
        kind: "interrupted_current_node",
        setupOperationID: parseSetupOperationID(setupOperationID),
      },
    },
  };
  return attentionToastID(attentionNotificationSurfaceKey(notification));
}

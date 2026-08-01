import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AppRoot } from "./AppRoot";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes, type TestAppServices } from "@/test-support/app-services";
import {
  flushQueuedWork,
  installAnimationFrameTestSupport,
  waitForMacrotask,
} from "@/test-support/scheduling";
import {
  workflowAttentionCalls,
  workflowAttentionRpcMethods,
} from "@/test-support/workflow-attention";

describe("application reconnect attention refresh", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    clearRoutePersistence();
    installAnimationFrameTestSupport();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    window.history.replaceState(null, "", "/");
    clearRoutePersistence();
  });

  it("coordinates central reconnect refresh with replacement subscription readiness", async () => {
    const services = createTestServices(startupRoutes);
    render(<AppRoot services={services} />);
    await flushQueuedWork();

    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(1);
    });
    expect(workflowAttentionCalls(services.transport)).toHaveLength(0);
    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
    });
    await flushQueuedWork();

    await act(async () => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
      await waitForMacrotask();
    });
    await waitFor(() => {
      expect(workflowAttentionCalls(services.transport)).toHaveLength(1);
    });

    await act(async () => {
      services.transport.connection.set("disconnected");
      await waitForMacrotask();
    });
    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(0);
    });
    await flushQueuedWork();

    await act(async () => {
      services.transport.connection.set("connected");
      await waitForMacrotask();
    });
    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(1);
    });
    expect(workflowAttentionCalls(services.transport)).toHaveLength(1);

    await act(async () => {
      services.transport.open(workflowAttentionRpcMethods.subscribeProject);
      await waitForMacrotask();
    });
    await waitFor(() => {
      expect(workflowAttentionCalls(services.transport)).toHaveLength(2);
    });
    await flushQueuedWork();
  });
});

function activeProjectSubscriptions(services: TestAppServices) {
  return services.transport.subscriptions.filter(
    (subscription) => subscription.method === workflowAttentionRpcMethods.subscribeProject,
  );
}

function clearRoutePersistence(): void {
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
}

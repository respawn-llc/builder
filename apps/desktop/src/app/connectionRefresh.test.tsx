import { act, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AppRoot } from "./AppRoot";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes, type TestAppServices } from "@/test-support/app-services";
import { flushQueuedWork, installAnimationFrameTestSupport } from "@/test-support/scheduling";

const globalAttentionMethod = "workflow.attention.list";
const globalSubscriptionMethod = "workflow.subscribeProject";

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

    await waitFor(() => {
      expect(attentionCalls(services)).toHaveLength(1);
    });
    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(1);
    });

    act(() => {
      services.transport.open(globalSubscriptionMethod);
    });
    await flushQueuedWork();
    expect(attentionCalls(services)).toHaveLength(1);

    act(() => {
      services.transport.connection.set("disconnected");
    });
    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(0);
    });

    act(() => {
      services.transport.connection.set("connected");
    });
    await waitFor(() => {
      expect(activeProjectSubscriptions(services)).toHaveLength(1);
    });
    await waitFor(() => {
      expect(attentionCalls(services)).toHaveLength(2);
    });

    act(() => {
      services.transport.open(globalSubscriptionMethod);
    });
    await flushQueuedWork();
    expect(attentionCalls(services)).toHaveLength(2);
  });
});

function attentionCalls(services: TestAppServices) {
  return services.transport.calls.filter((call) => call.method === globalAttentionMethod);
}

function activeProjectSubscriptions(services: TestAppServices) {
  return services.transport.subscriptions.filter((subscription) => subscription.method === globalSubscriptionMethod);
}

function clearRoutePersistence(): void {
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
}

import { act, render, screen, waitFor } from "@testing-library/react";
import { useQuery } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { queryKeys } from "@/app-facade";
import { AppRoot } from "./AppRoot";
import { AppProviders } from "./AppProviders";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import {
  flushQueuedWork,
  installAnimationFrameTestSupport,
  waitForMacrotask,
} from "@/test-support/scheduling";
import { workflowAttentionCalls } from "@/test-support/workflow-attention";

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

  it("refreshes the active attention projection after reconnect", async () => {
    const services = createTestServices(startupRoutes);
    render(<AppRoot services={services} />);

    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
    });
    await waitFor(() => {
      expect(workflowAttentionCalls(services.transport)).toHaveLength(1);
    });

    await act(async () => {
      services.transport.connection.set("disconnected");
      await waitForMacrotask();
    });

    await act(async () => {
      services.transport.connection.set("connected");
      await waitForMacrotask();
    });
    await waitFor(() => {
      expect(workflowAttentionCalls(services.transport)).toHaveLength(2);
    });
    await flushQueuedWork();
  });

  it("refreshes an active Project Workspace catalog after reconnect", async () => {
    const services = createTestServices(startupRoutes);
    const loadCatalog = vi.fn(async () => ({ workspaces: [] }));

    function ActiveWorkspaceCatalog() {
      useQuery({
        queryKey: queryKeys.projectWorkspaceCatalog("project-1"),
        queryFn: loadCatalog,
      });
      return null;
    }

    render(
      <AppProviders services={services}>
        <ActiveWorkspaceCatalog />
      </AppProviders>,
    );
    await waitFor(() => {
      expect(loadCatalog).toHaveBeenCalledTimes(1);
    });

    await act(async () => {
      services.transport.connection.set("disconnected");
      await waitForMacrotask();
    });
    await act(async () => {
      services.transport.connection.set("connected");
      await waitForMacrotask();
    });

    await waitFor(() => {
      expect(loadCatalog).toHaveBeenCalledTimes(2);
    });
  });
});

function clearRoutePersistence(): void {
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
}

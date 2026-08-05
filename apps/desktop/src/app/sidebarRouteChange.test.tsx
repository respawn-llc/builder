import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import type * as TanStackRouter from "@tanstack/react-router";

import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { SidebarHost, SidebarRouteChangeCloser } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";
import { useSidebar } from "@/app-facade";

type HistoryListener = (event: Readonly<{ location: Readonly<{ pathname: string }> }>) => void;

const routeState = vi.hoisted(() => ({
  listeners: new Set<HistoryListener>(),
  pathname: "/projects/project-1",
}));

vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<typeof TanStackRouter>("@tanstack/react-router");
  return {
    ...actual,
    useRouter: () => ({
      history: {
        subscribe(listener: HistoryListener) {
          routeState.listeners.add(listener);
          return () => {
            routeState.listeners.delete(listener);
          };
        },
      },
      state: { location: routeState },
    }),
  };
});

describe("SidebarRouteChangeCloser", () => {
  beforeEach(() => {
    routeState.pathname = "/projects/project-1";
    routeState.listeners.clear();
  });

  it.each(["/", "/projects/project-2"])(
    "closes the complete sidebar when the browser pathname changes to %s",
    async (nextPathname) => {
      const services = createTestServices([]);
      render(
        <TestAppProviders services={services}>
          <SidebarProvider>
            <SidebarRouteChangeCloser />
            <SidebarHost />
            <OpenSidebar />
          </SidebarProvider>
        </TestAppProviders>,
      );

      await waitFor(() =>
        expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
      );
      act(() => {
        publishPathname(nextPathname);
      });

      await waitFor(() =>
        expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
      );
    },
  );

  it("does not close for a search-only route change", async () => {
    const services = createTestServices([]);
    render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarRouteChangeCloser />
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"),
    );
    act(() => {
      publishPathname("/projects/project-1");
    });

    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open");
  });
});

function publishPathname(pathname: string): void {
  routeState.pathname = pathname;
  for (const listener of routeState.listeners) {
    listener({ location: routeState });
  }
}

function OpenSidebar() {
  const { openSidebar } = useSidebar();
  useEffect(() => {
    void openSidebar({
      content: <div>Task</div>,
      kind: "custom",
      title: "Task",
    });
  }, [openSidebar]);
  return null;
}

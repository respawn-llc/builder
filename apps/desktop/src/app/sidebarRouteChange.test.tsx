import { render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import type * as TanStackRouter from "@tanstack/react-router";

import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import { SidebarHost, SidebarRouteChangeCloser } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";
import { useSidebar } from "@/app-facade";

const routeState = vi.hoisted(() => ({ pathname: "/projects/project-1" }));

vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<typeof TanStackRouter>("@tanstack/react-router");
  return {
    ...actual,
    useLocation: () => routeState,
  };
});

describe("SidebarRouteChangeCloser", () => {
  beforeEach(() => {
    routeState.pathname = "/projects/project-1";
  });

  it("closes the complete sidebar when the browser pathname changes", async () => {
    const services = createTestServices([]);
    const view = render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarRouteChangeCloser />
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
    );

    await waitFor(() => expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "open"));
    routeState.pathname = "/";
    view.rerender(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <SidebarRouteChangeCloser />
          <SidebarHost />
          <OpenSidebar />
        </SidebarProvider>
      </TestAppProviders>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing"),
    );
  });
});

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

import { render, screen } from "@testing-library/react";
import { useEffect } from "react";
import type * as TanStackRouter from "@tanstack/react-router";

import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { useSidebar } from "@/app-facade";
import { SidebarHost } from "./sidebar";
import { SidebarProvider } from "./sidebarProvider";
import { WorkflowEditorShellRoute } from "./routeComponents";

const routeState = vi.hoisted(() => ({ projectID: "project-1" }));

vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<typeof TanStackRouter>("@tanstack/react-router");
  const fallbackRouteApi = {
    useNavigate: () => vi.fn(),
    useParams: () => ({}),
    useSearch: () => ({}),
  };
  return {
    ...actual,
    getRouteApi: (routeID: string) =>
      routeID === "/workflows/$workflowId/editor"
        ? {
            useParams: () => ({ workflowId: "workflow-1" }),
            useSearch: () => ({ projectId: routeState.projectID }),
          }
        : fallbackRouteApi,
  };
});

vi.mock("@/features/workflow-editor", () => ({
  WorkflowEditorRoute: () => null,
}));

describe("Workflow Editor route ownership", () => {
  it("closes an unrelated sidebar on a validated projectId-only transition", async () => {
    const services = createTestServices([]);
    const { rerender } = render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <RouteScenario projectID="project-1" />
        </SidebarProvider>
      </TestAppProviders>,
    );

    await screen.findByTestId("app-sidebar-host");
    routeState.projectID = "project-2";
    rerender(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <RouteScenario projectID="project-2" />
        </SidebarProvider>
      </TestAppProviders>,
    );

    expect(screen.getByTestId("app-sidebar-host")).toHaveAttribute("data-state", "closing");
  });
});

function RouteScenario({ projectID }: Readonly<{ projectID: string }>) {
  const { openSidebar } = useSidebar();
  useEffect(() => {
    void openSidebar({
      content: <div>Notification</div>,
      kind: "custom",
      title: "Notification",
    });
  }, [openSidebar]);
  return (
    <>
      <WorkflowEditorShellRoute />
      <SidebarHost />
      <output data-testid="workflow-editor-project">{projectID}</output>
    </>
  );
}

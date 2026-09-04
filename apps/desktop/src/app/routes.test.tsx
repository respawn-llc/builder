import { render, screen, waitFor } from "@testing-library/react";
import { RouterProvider } from "@tanstack/react-router";
import { afterEach, expect, it, vi } from "vitest";

import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { sessionChatRoutePath } from "@/app-facade";
import { createAppRouter } from "./routes";

const fixture = vi.hoisted(() => ({
  probeCatalogReturn: false,
  triggerCatalogNavigation: false,
}));

vi.mock("./routeComponents", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  const { useEffect } = await import("react");
  const { SessionChatCatalogReturnProvider, useAppNavigation, useSessionChatCatalogReturn } =
    await import("@/app-facade");
  return {
    HomeShellRoute: () => null,
    ChatRoute: () => <div data-testid="standalone-chat-route" />,
    ProjectRoute: () => null,
    ProjectTasksRoute: () =>
      fixture.triggerCatalogNavigation ? (
        <CatalogNavigationProbe />
      ) : fixture.probeCatalogReturn ? (
        <CatalogReturnProbe />
      ) : (
        <div data-testid="standalone-project-tasks-route" />
      ),
    RootRoute: () => (
      <SessionChatCatalogReturnProvider>
        <Outlet />
      </SessionChatCatalogReturnProvider>
    ),
    TaskRoute: () => null,
    WorkflowEditorShellRoute: () => null,
    WorkflowLibraryShellRoute: () => null,
  };

  function CatalogNavigationProbe() {
    const navigation = useAppNavigation();
    useEffect(() => {
      fixture.triggerCatalogNavigation = false;
      void navigation.openSessionChat({
        catalogOrigin: { category: "main" },
        projectID: "project-1",
        sessionID: "session-1",
      });
    }, [navigation]);
    return <div data-testid="standalone-project-tasks-route" />;
  }

  function CatalogReturnProbe() {
    const catalogReturn = useSessionChatCatalogReturn("project-1");
    return <div data-category={catalogReturn?.category ?? "none"} data-testid="catalog-return-probe" />;
  }
});

afterEach(() => {
  fixture.probeCatalogReturn = false;
  fixture.triggerCatalogNavigation = false;
  window.history.replaceState(null, "", "/");
});

it("rejects malformed present workflow selectors while preserving omission", () => {
  const validate = createAppRouter().routesById["/projects/$projectId"].options.validateSearch;
  if (!(validate instanceof Function)) {
    throw new Error("project route search validation is unavailable");
  }
  expect(validate({})).toEqual({ taskId: "", workflowId: undefined });
  expect(validate({ workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0" })).toEqual({
    taskId: "",
    workflowId: "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0",
  });
  expect(() => validate({ workflowId: "workflow-1" })).toThrow();
});

it("normalizes omitted Home project selection", () => {
  const validate = createAppRouter().routesById["/"].options.validateSearch;
  if (!(validate instanceof Function)) {
    throw new Error("Home route search validation is unavailable");
  }
  expect(validate({})).toEqual({});
  expect(validate({ projectId: "project-1" })).toEqual({ projectId: "project-1" });
});

it("opens the standalone Project Task List route from its public URL", async () => {
  window.history.replaceState(null, "", "/projects/project-1/tasks");
  render(<RouterProvider router={createAppRouter()} />);

  expect(await screen.findByTestId("standalone-project-tasks-route")).toBeInTheDocument();
});

it("opens the development-gated Session Chat route and uses ordinary Back", async () => {
  window.history.replaceState(null, "", "/projects/project-1/tasks");
  const router = createAppRouter();
  render(<RouterProvider router={router} />);

  await router.navigate({
    to: sessionChatRoutePath,
    params: { projectId: "project-1", sessionId: "session-1" },
  });

  expect(await screen.findByTestId("standalone-chat-route")).toBeInTheDocument();
  expect(window.location.pathname).toBe("/projects/project-1/sessions/session-1");

  router.history.back();
  await waitFor(() => {
    expect(window.location.pathname).toBe("/projects/project-1/tasks");
  });
});

it("opens a direct Session Chat URL without a catalog origin", async () => {
  window.history.replaceState(null, "", "/projects/project-1/sessions/session-1");
  const router = createAppRouter();
  render(<RouterProvider router={router} />);

  expect(await screen.findByTestId("standalone-chat-route")).toBeInTheDocument();
  expect(Object.hasOwn(router.state.location.state, "sessionChat")).toBe(false);
});

it("transports a catalog origin through the real AppNavigation route transition", async () => {
  fixture.triggerCatalogNavigation = true;
  window.history.replaceState(null, "", "/projects/project-1/tasks");
  const router = createAppRouter();
  render(
    <TestAppProviders services={createTestServices([])}>
      <RouterProvider router={router} />
    </TestAppProviders>,
  );

  expect(await screen.findByTestId("standalone-chat-route")).toBeInTheDocument();
  expect(router.state.location.state).toMatchObject({
    sessionChat: {
      catalogOrigin: { category: "main" },
      projectID: "project-1",
    },
  });
});

it("restores the originating catalog after returning from Session Chat", async () => {
  fixture.probeCatalogReturn = true;
  fixture.triggerCatalogNavigation = true;
  window.history.replaceState(null, "", "/projects/project-1/tasks");
  const router = createAppRouter();
  render(
    <TestAppProviders services={createTestServices([])}>
      <RouterProvider router={router} />
    </TestAppProviders>,
  );

  expect(await screen.findByTestId("standalone-chat-route")).toBeInTheDocument();
  router.history.back();

  await waitFor(() => {
    expect(screen.getByTestId("catalog-return-probe")).toHaveAttribute("data-category", "main");
  });
});

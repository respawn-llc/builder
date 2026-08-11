import { QueryClient } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { z } from "zod";

import { queryKeys } from "@/app-facade";
import { ProjectWorkspaceTabProvider } from "@/app-facade";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import { ProjectWorkspaceRoute } from "./ProjectWorkspace";

vi.mock("@/features/board", () => ({
  BoardRoute: () => <div data-testid="workflows-body">Workflows body</div>,
}));

vi.mock("@/features/sessions", () => ({
  ProjectSessionsBrowser: () => <div data-testid="sessions-body">Sessions body</div>,
}));

const projectEditResult = {
  project_id: "project-1",
  project_key: "KNT",
  display_name: "Kent",
  default_workspace_id: "workspace-1",
  workspaces: [],
  next_page_token: "",
};

it("opens both first-selected and restored Projects on Sessions", async () => {
  const view = renderWorkspace([{ method: "project.edit.get", result: projectEditResult }]);
  expect(await screen.findByTestId("sessions-body")).toBeInTheDocument();
  view.unmount();

  renderWorkspace([{ method: "project.edit.get", result: projectEditResult }]);
  expect(await screen.findByTestId("sessions-body")).toBeInTheDocument();
});

it("retains the selected Project tab while moving between Projects", async () => {
  const routes: FakeRoute[] = [
    {
      method: "project.edit.get",
      handler: (params) => ({
        ...projectEditResult,
        project_id: z.object({ project_id: z.string() }).parse(params).project_id,
      }),
    },
  ];
  const view = renderWorkspace(routes);
  fireEvent.click(await screen.findByRole("tab", { name: "Workflows" }));
  expect(await screen.findByTestId("workflows-body")).toBeInTheDocument();

  view.rerender(
    workspaceProviders(
      routes,
      <ProjectWorkspaceRoute projectId="project-2" selectedTaskId="" workflowId={undefined} />,
      view.queryClient,
    ),
  );
  expect(await screen.findByTestId("workflows-body")).toBeInTheDocument();
  expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
});

it("renders whole-workspace loading and retryable failure without identity", async () => {
  const pending = new Promise<never>(() => undefined);
  {
    const view = renderWorkspace([
      { method: "project.edit.get", handler: async () => pending },
    ]);
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    view.unmount();
  }

  const view = renderWorkspace([
    { method: "project.edit.get", error: new Error("identity unavailable") },
  ]);
  expect(await screen.findByText("identity unavailable")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await waitFor(() => {
    expect(
      view.services.transport.calls.filter((call) => call.method === "project.edit.get"),
    ).toHaveLength(2);
  });
});

it("replaces cached identity content when a refresh fails", async () => {
  const queryClient = testQueryClient();
  queryClient.setQueryData(queryKeys.projectEdit("project-1"), {
    pages: [
      {
        projectID: "project-1",
        projectKey: "KNT",
        displayName: "Cached Kent",
        defaultWorkspaceID: "workspace-1",
        workspaces: [],
        nextPageToken: "",
      },
    ],
    pageParams: [""],
  });
  renderWorkspace(
    [{ method: "project.edit.get", error: new Error("refresh failed") }],
    queryClient,
  );

  expect(await screen.findByText("refresh failed")).toBeInTheDocument();
  expect(screen.queryByText("Cached Kent")).not.toBeInTheDocument();
  expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
});

function renderWorkspace(routes: readonly FakeRoute[], queryClient = testQueryClient()) {
  const services = createTestServices(routes);
  const view = render(
    workspaceProviders(
      routes,
      <ProjectWorkspaceRoute projectId="project-1" selectedTaskId="" workflowId={undefined} />,
      queryClient,
      services,
    ),
  );
  return { ...view, queryClient, services };
}

function workspaceProviders(
  routes: readonly FakeRoute[],
  children: ReactNode,
  queryClient: QueryClient,
  services = createTestServices(routes),
) {
  return (
    <TestAppProviders queryClient={queryClient} services={services}>
      <ProjectWorkspaceTabProvider>{children}</ProjectWorkspaceTabProvider>
    </TestAppProviders>
  );
}

function testQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false, staleTime: 0 },
    },
  });
}

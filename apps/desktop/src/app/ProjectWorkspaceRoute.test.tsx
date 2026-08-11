import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { z } from "zod";

import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { AppRoot } from "./AppRoot";

vi.mock("@/features/board", async (importOriginal) => ({
  ...(await importOriginal()),
  BoardRoute: ({ projectId }: Readonly<{ projectId: string }>) => (
    <div data-testid="workflows-body">{projectId}</div>
  ),
}));
vi.mock("@/features/sessions", () => ({
  ProjectSessionsBrowser: ({ projectID }: Readonly<{ projectID: string }>) => (
    <div data-testid="sessions-body">{projectID}</div>
  ),
}));

describe("Project workspace route", () => {
  beforeEach(async () => {
    const { JSDOM } = await vi.importActual<{ JSDOM: JSDOMConstructor }>("jsdom");
    const storageWindow = new JSDOM("", { url: "http://localhost" }).window;
    vi.stubGlobal("localStorage", storageWindow.localStorage);
    vi.stubGlobal("sessionStorage", storageWindow.sessionStorage);
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    window.history.replaceState(null, "", "/");
    vi.unstubAllGlobals();
  });

  it("restores on Sessions, carries Workflows between Projects, and resets after remount", async () => {
    localStorage.setItem("desktop.lastProjectRoute", JSON.stringify({ projectId: "project-1" }));
    const view = render(<AppRoot services={createTestServices(routes(["project-2"]))} />);

    const workspace = await screen.findByTestId("project-workspace");
    expect(screen.getByTestId("sessions-body")).toHaveTextContent("project-1");
    fireEvent.click(within(workspace).getByRole("tab", { selected: false }));
    expect(await screen.findByTestId("workflows-body")).toHaveTextContent("project-1");
    expect(new URL(window.location.href).searchParams.has("tab")).toBe(false);

    fireEvent.click(within(screen.getByTestId("app-chrome-navigation")).getByRole("link"));
    fireEvent.click(await screen.findByTestId("home-list-card-button"));
    expect(await screen.findByTestId("workflows-body")).toHaveTextContent("project-2");

    view.unmount();
    mount("/projects/project-2");
    expect(await screen.findByTestId("sessions-body")).toHaveTextContent("project-2");
  });

  it("starts Home on Projects and opens a manually selected Project on Sessions", async () => {
    mount("/", routes(["project-1"]));

    const home = await screen.findByTestId("home-primary-projects-tab-island");
    const projectsTab = within(home).getByRole("tab");
    expect(projectsTab).toHaveAttribute("aria-selected", "true");
    fireEvent.click(await screen.findByTestId("home-list-card-button"));
    await screen.findByTestId("project-workspace");
    expect(screen.getByRole("heading", { name: "Project project-1" })).toBeInTheDocument();
    expect(screen.getByText("KEY-project-1")).toBeInTheDocument();
    expect(await screen.findByTestId("sessions-body")).toHaveTextContent("project-1");
    expect(screen.queryByTestId("workflows-body")).not.toBeInTheDocument();
  });

  it("blocks both bodies while identity loads or a cached refresh fails", async () => {
    const pending = new Promise<never>(() => undefined);
    const loading = mount("/projects/project-1", [
      ...startupRoutes,
      { method: "project.edit.get", handler: async () => pending },
    ]);
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
    loading.unmount();

    let failIdentity = false;
    const services = mount("/projects/project-1", [
      ...startupRoutes,
      {
        method: "project.edit.get",
        handler: () => {
          if (failIdentity) throw new Error("identity refresh failed");
          return projectEditFixture("project-1");
        },
      },
    ]);
    expect(await screen.findByTestId("sessions-body")).toBeInTheDocument();
    failIdentity = true;
    await act(async () => {
      services.transport.connection.set("disconnected");
      await Promise.resolve();
      services.transport.connection.set("connected");
      await Promise.resolve();
    });

    expect(await screen.findByTestId("error-state", {}, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.queryByTestId("project-workspace")).not.toBeInTheDocument();
    expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
    failIdentity = false;
    fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
    expect(await screen.findByTestId("sessions-body")).toBeInTheDocument();
  });
});

type JSDOMConstructor = new (html?: string, options?: { url?: string }) => { window: Window };

function mount(path: string, fakeRoutes = routes()) {
  window.history.replaceState(null, "", path);
  const services = createTestServices(fakeRoutes);
  return Object.assign(render(<AppRoot services={services} />), services);
}

function routes(projectIDs = ["project-1", "project-2"]) {
  return [
    ...startupRoutes,
    {
      method: "project.home.list",
      result: {
        projects: projectIDs.map(projectFixture),
        next_page_token: "",
        generated_at_unix_ms: 1,
      },
    },
    {
      method: "project.edit.get",
      handler: (params: unknown) => {
        const projectID = z.object({ project_id: z.string() }).parse(params).project_id;
        return projectEditFixture(projectID);
      },
    },
  ];
}

function projectEditFixture(projectID: string) {
  return {
    project_id: projectID,
    project_key: `KEY-${projectID}`,
    display_name: `Project ${projectID}`,
    default_workspace_id: `workspace-${projectID}`,
    workspaces: [],
    next_page_token: "",
  };
}

function projectFixture(projectID: string) {
  return {
    project_id: projectID,
    project_key: `KEY-${projectID}`,
    display_name: `Project ${projectID}`,
    primary_workspace: {
      workspace_id: `workspace-${projectID}`,
      display_name: `Workspace ${projectID}`,
      root_path: `/workspace/${projectID}`,
      availability: "available",
      is_primary: true,
      updated_at_unix_ms: 1,
    },
    default_workflow_id: null,
    default_workflow_name: "",
    default_workflow_valid: false,
    updated_at_unix_ms: 1,
    task_count: 0,
    attention_count: 0,
    workflow_count: 0,
  };
}

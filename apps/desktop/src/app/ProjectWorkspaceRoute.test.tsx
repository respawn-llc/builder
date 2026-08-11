import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
    vi.stubGlobal("sessionStorage", memoryStorage());
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    window.history.replaceState(null, "", "/");
    vi.unstubAllGlobals();
  });

  it("restores on Sessions, carries Workflows between Projects, and resets after remount", async () => {
    localStorage.setItem("desktop.lastProjectRoute", JSON.stringify({ projectId: "project-1" }));
    const view = render(<AppRoot services={createTestServices(routes())} />);

    const workspace = await screen.findByTestId("project-workspace");
    expect(screen.getByTestId("sessions-body")).toHaveTextContent("project-1");
    fireEvent.click(requiredElement(within(workspace).getAllByRole("tab"), 0));
    expect(await screen.findByTestId("workflows-body")).toHaveTextContent("project-1");
    expect(new URL(window.location.href).searchParams.has("tab")).toBe(false);

    fireEvent.click(within(screen.getByTestId("app-chrome-navigation")).getByRole("link"));
    const projects = await screen.findAllByTestId("home-list-card-button");
    fireEvent.click(requiredElement(projects, 1));
    expect(await screen.findByTestId("workflows-body")).toHaveTextContent("project-2");

    view.unmount();
    mount("/projects/project-2");
    expect(await screen.findByTestId("sessions-body")).toHaveTextContent("project-2");
  });

  it("starts Home on Projects and opens a manually selected Project on Sessions", async () => {
    mount("/");

    const home = await screen.findByTestId("home-primary-projects-tab-island");
    const projectsTab = within(home).getByRole("tab");
    expect(projectsTab).toHaveAttribute("aria-selected", "true");
    fireEvent.click(requiredElement(await screen.findAllByTestId("home-list-card-button"), 0));
    await screen.findByTestId("project-workspace");
    expect(screen.getByRole("heading", { name: "Project project-1" })).toBeInTheDocument();
    expect(screen.getByText("KEY-project-1")).toBeInTheDocument();
    expect(await screen.findByTestId("sessions-body")).toHaveTextContent("project-1");
    expect(screen.queryByTestId("workflows-body")).not.toBeInTheDocument();
  });

  it("blocks both bodies with identity Loading and retryable Error", async () => {
    const pending = new Promise<never>(() => undefined);
    const loading = mount("/projects/project-1", [
      ...startupRoutes,
      { method: "project.edit.get", handler: async () => pending },
    ]);
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
    loading.unmount();

    const services = createTestServices([
      ...startupRoutes,
      { method: "project.edit.get", error: new Error("identity unavailable") },
    ]);
    render(<AppRoot services={services} />);
    expect(await screen.findByTestId("error-state", {}, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
    fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "project.edit.get").length,
      ).toBeGreaterThan(1);
    });
  });
});

function mount(path: string, fakeRoutes = routes()) {
  window.history.replaceState(null, "", path);
  const services = createTestServices(fakeRoutes);
  return Object.assign(render(<AppRoot services={services} />), services);
}

function routes() {
  return [
    ...startupRoutes,
    {
      method: "project.home.list",
      result: {
        projects: ["project-1", "project-2"].map(projectFixture),
        next_page_token: "",
        generated_at_unix_ms: 1,
      },
    },
    {
      method: "project.edit.get",
      handler: (params: unknown) => {
        const projectID = z.object({ project_id: z.string() }).parse(params).project_id;
        return {
          project_id: projectID,
          project_key: `KEY-${projectID}`,
          display_name: `Project ${projectID}`,
          default_workspace_id: `workspace-${projectID}`,
          workspaces: [],
          next_page_token: "",
        };
      },
    },
  ];
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

function requiredElement(elements: readonly HTMLElement[], index: number): HTMLElement {
  const element = elements[index];
  if (element === undefined) throw new Error(`Required element ${index.toString()} is unavailable.`);
  return element;
}

function memoryStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    clear() {
      values.clear();
    },
    getItem(key) {
      return values.get(key) ?? null;
    },
    key(index) {
      return [...values.keys()][index] ?? null;
    },
    removeItem(key) {
      values.delete(key);
    },
    setItem(key, value) {
      values.set(key, value);
    },
  };
}

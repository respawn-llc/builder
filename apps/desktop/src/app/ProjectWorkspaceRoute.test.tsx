import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { AppRoot } from "./AppRoot";
import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { MemoryStorage } from "@/test-support/browser-storage";
import { getSelectedTabs, getUnselectedTab } from "@/test-support/tabs";

const bodyMounts = vi.hoisted(() => ({
  sessions: vi.fn(),
  workflows: vi.fn(),
}));

vi.mock("@/features/board", async (importOriginal) => ({
  ...(await importOriginal()),
  BoardRoute: () => {
    bodyMounts.workflows();
    return <div data-testid="workflows-body">Workflows body</div>;
  },
}));

vi.mock("@/features/sessions", () => ({
  ProjectSessionsBrowser: () => {
    bodyMounts.sessions();
    return <div data-testid="sessions-body">Sessions body</div>;
  },
}));

describe("Project workspace route composition", () => {
  beforeEach(() => {
    bodyMounts.sessions.mockClear();
    bodyMounts.workflows.mockClear();
    vi.stubGlobal("localStorage", new MemoryStorage());
    vi.stubGlobal("sessionStorage", new MemoryStorage());
  });

  afterEach(() => {
    window.history.replaceState(null, "", "/");
    removeBrowserStorage("local", "desktop.lastProjectRoute");
    removeBrowserStorage("session", "desktop.routeRestoreChecked");
    vi.unstubAllGlobals();
  });

  it("mounts the sticky identity header and only Sessions by default", async () => {
    window.history.replaceState(null, "", "/projects/project-1");
    const services = createTestServices(routes());
    render(<AppRoot services={services} />);

    expect(await screen.findByRole("heading", { name: "Kent" }, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.getByText("KNT")).toBeInTheDocument();
    const workspace = screen.getByTestId("project-workspace");
    expect(getSelectedTabs(workspace)).toHaveLength(1);
    expect(screen.getByTestId("sessions-body")).toBeInTheDocument();
    expect(screen.queryByTestId("workflows-body")).not.toBeInTheDocument();
    expect(bodyMounts.sessions).toHaveBeenCalled();
    expect(bodyMounts.workflows).not.toHaveBeenCalled();
    expect(within(workspace).queryByRole("button")).not.toBeInTheDocument();
    expect(services.transport.calls.filter((call) => call.method === "workflow.board.get")).toHaveLength(0);
  });

  it("restores a Project into Sessions and mounts only Workflows after tab selection", async () => {
    window.history.replaceState(null, "", "/");
    localStorage.setItem("desktop.lastProjectRoute", JSON.stringify({ projectId: "project-1" }));
    const services = createTestServices(routes());
    render(<AppRoot services={services} />);

    const workspace = await screen.findByTestId("project-workspace");
    const destination = window.location.href;
    expect(new URL(destination).searchParams.has("tab")).toBe(false);
    fireEvent.click(getUnselectedTab(workspace));
    expect(await screen.findByTestId("workflows-body")).toBeInTheDocument();
    expect(screen.queryByTestId("sessions-body")).not.toBeInTheDocument();
    expect(window.location.href).toBe(destination);
    await waitFor(() => {
      expect(bodyMounts.workflows).toHaveBeenCalled();
    });
  });

  it("opens a manually selected Home Project on Sessions", async () => {
    window.history.replaceState(null, "", "/");
    const services = createTestServices([
      ...routes(),
      {
        method: "project.home.list",
        result: {
          projects: [
            {
              project_id: "project-1",
              project_key: "KNT",
              display_name: "Kent",
              primary_workspace: {
                workspace_id: "workspace-1",
                display_name: "Kent",
                root_path: "/workspace/kent",
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
            },
          ],
          next_page_token: "",
          generated_at_unix_ms: 1,
        },
      },
    ]);
    render(<AppRoot services={services} />);

    fireEvent.click(await screen.findByTestId("home-list-card-button"));
    expect(await screen.findByTestId("sessions-body")).toBeInTheDocument();
  });
});

function routes() {
  return [
    ...startupRoutes,
    {
      method: "project.edit.get",
      result: {
        project_id: "project-1",
        project_key: "KNT",
        display_name: "Kent",
        default_workspace_id: "workspace-1",
        workspaces: [],
        next_page_token: "",
      },
    },
  ];
}

import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeDirectorySelection,
} from "@app/native-bridge";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach } from "vitest";

import { App } from "./App";
import { createTestServices, startupRoutes } from "@/test-support/app-services";

describe("App", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    window.history.pushState(null, "", "/");
    clearStorage("localStorage");
    clearStorage("sessionStorage");
    document.documentElement.removeAttribute("data-theme");
    globalThis.ResizeObserver = originalResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
    document.documentElement.removeAttribute("data-theme");
  });

  it("renders the startup-gated home shell", async () => {
    render(<App services={createTestServices(startupRoutes)} />);

    expect(await screen.findByTestId("home-route-root")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Projects" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Workflows" })).toHaveAttribute("aria-selected", "false");
  });

  it("switches the Home left pane from projects to workflows", async () => {
    render(
      <App
        services={createTestServices([
          ...startupRoutes,
          {
            method: "workflow.list",
            result: {
              workflows: [
                {
                  id: "workflow-1",
                  name: "Delivery",
                  description: "Ship changes",
                  version: 1,
                },
              ],
              next_page_token: "",
            },
          },
        ])}
      />,
    );

    fireEvent.click(await screen.findByRole("tab", { name: "Workflows" }));

    expect(window.location.pathname).toBe("/");
    expect(screen.getByRole("tab", { name: "Workflows" })).toHaveAttribute("aria-selected", "true");
  });

  it("disables Workflow Library creation while disconnected", async () => {
    window.history.pushState(null, "", "/workflows");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.list", result: { workflows: [], next_page_token: "" } },
    ]);
    services.transport.connection.set("disconnected", "offline");

    render(<App services={services} />);

    const createButtons = await screen.findAllByRole("button", { name: "Create workflow" });
    expect(createButtons).toHaveLength(1);
    expect(createButtons.every((button) => button.hasAttribute("disabled"))).toBe(true);
  });

  it("creates projects from a validated dialog destination", async () => {
    const services = createTestServices(
      [
        ...startupRoutes,
        {
          method: "project.planWorkspaceBinding",
          result: {
            kind: "local_unbound",
            canonical_root: "/tmp/example-project",
            binding: null,
          },
        },
        {
          method: "project.create",
          result: {
            binding: {
              project_id: "project-1",
              project_key: "EXP",
              display_name: "Example Project",
              workspace_id: "workspace-1",
              canonical_root: "/tmp/example-project",
              workspace_name: "example-project",
              workspace_status: "available",
            },
          },
        },
      ],
      directoryBridge("/tmp/example-project"),
    );

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: "New Project" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Project name"), { target: { value: " Example Project " } });
    fireEvent.change(screen.getByLabelText("Project key"), { target: { value: "x ?" } });

    fireEvent.change(screen.getByLabelText("Project name"), { target: { value: "Example Project" } });
    fireEvent.change(screen.getByLabelText("Project key"), { target: { value: "exp" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "project.create",
        params: {
          display_name: "Example Project",
          project_key: "EXP",
          workspace_root: "/tmp/example-project",
        },
      });
    });
  });

  it("falls back to the dialog when the project creation window is denied", async () => {
    const services = createTestServices(
      [
        ...startupRoutes,
        {
          method: "project.planWorkspaceBinding",
          result: {
            kind: "local_unbound",
            canonical_root: "/tmp/example-project",
            binding: null,
          },
        },
      ],
      projectCreationWindowBridge("/tmp/example-project", { message: "window.get_all_windows not allowed" }),
    );

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: "New Project" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("does not create projects for server workspace selection plans", async () => {
    const services = createTestServices(
      [
        ...startupRoutes,
        {
          method: "project.planWorkspaceBinding",
          result: {
            kind: "server_workspace_selection",
            canonical_root: "/tmp/example-project",
            binding: null,
          },
        },
        {
          method: "project.create",
          result: {
            binding: {
              project_id: "project-created",
              project_key: "EXP",
              display_name: "Example Project",
              workspace_id: "workspace-created",
              canonical_root: "/tmp/example-project",
              workspace_name: "example-project",
              workspace_status: "available",
            },
          },
        },
      ],
      directoryBridge("/tmp/example-project"),
    );

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: "New Project" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(services.transport.calls).not.toContainEqual({
      method: "project.create",
      params: {
        display_name: "Example Project",
        project_key: "EXP",
        workspace_root: "/tmp/example-project",
      },
    });
  });
});

function directoryBridge(path: string): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      directories: { select: true },
    },
    directories: {
      async selectDirectory(): Promise<NativeDirectorySelection> {
        return { path };
      },
    },
  };
}

function projectCreationWindowBridge(path: string, error: unknown): NativeBridge {
  const base = directoryBridge(path);
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      projectCreationWindow: true,
    },
    projectCreation: {
      ...base.projectCreation,
      async openWindow(): Promise<void> {
        throw error;
      },
    },
  };
}

function clearStorage(name: "localStorage" | "sessionStorage"): void {
  try {
    if (name === "localStorage") {
      globalThis.localStorage.clear();
      return;
    }
    globalThis.sessionStorage.clear();
  } catch {
    return;
  }
}

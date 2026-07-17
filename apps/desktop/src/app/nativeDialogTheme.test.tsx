import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

import { App } from "./startup/App";
import { createTestServices, startupRoutes } from "@/test-support/app-services";

describe("native dialog theme inheritance", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
    window.history.pushState(null, "", "/");
  });

  it("applies inherited native-dialog theme before rendering dialog routes", async () => {
    window.history.pushState(
      null,
      "",
      "/native-dialog/new-task?projectID=project-1&workflowID=workflow-1&__appTheme=light",
    );

    render(
      <App
        services={createTestServices([
          ...startupRoutes,
          {
            method: "project.workspace.list",
            result: {
              project_id: "project-1",
              workspaces: [],
              default_workspace_id: "",
              next_page_token: "",
            },
          },
        ])}
      />,
    );

    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    expect(await screen.findByTestId("native-dialog-content")).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });
});

import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { parseSessionPageRequest, sessionPageFixture } from "@/test-support/session-catalog";
import { AppRoot } from "./AppRoot";

afterEach(() => {
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
  window.history.replaceState(null, "", "/");
});

it("refreshes only the active category and surfaces a reconnect failure with Retry", async () => {
  window.history.replaceState(null, "", "/projects/project-1");
  const refreshedCategories: string[] = [];
  let collectRefresh = false;
  let failRefresh = false;
  const services = createTestServices([
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
    {
      method: "session.page",
      handler: (params) => {
        const request = parseSessionPageRequest(params);
        if (collectRefresh) refreshedCategories.push(request.category);
        if (request.category === "subagent" && failRefresh) throw new Error("refresh failed");
        return sessionPageFixture(request, [
          [
            request.category === "subagent" && collectRefresh
              ? "subagent-after-retry"
              : `${request.category}-before-refresh`,
          ],
        ]);
      },
    },
  ]);
  render(<AppRoot services={services} />);

  const browser = await screen.findByTestId("project-sessions-browser");
  fireEvent.click(within(browser).getByRole("tab", { selected: false }));
  expect(await screen.findByText("subagent-before-refresh")).toBeInTheDocument();
  collectRefresh = true;
  failRefresh = true;

  await act(async () => {
    services.transport.connection.set("disconnected");
    await Promise.resolve();
  });
  await act(async () => {
    services.transport.connection.set("connected");
    await Promise.resolve();
  });

  expect(await screen.findByTestId("error-state", {}, { timeout: 5_000 })).toBeInTheDocument();
  expect(new Set(refreshedCategories)).toEqual(new Set(["subagent"]));
  failRefresh = false;
  fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
  expect(await screen.findByText("subagent-after-retry")).toBeInTheDocument();
});

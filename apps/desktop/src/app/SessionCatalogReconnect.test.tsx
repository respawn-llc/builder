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
  const callsByCategory = new Map<string, number>();
  const refreshedCategories: string[] = [];
  let collectRefresh = false;
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
        const call = (callsByCategory.get(request.category) ?? 0) + 1;
        callsByCategory.set(request.category, call);
        if (collectRefresh) refreshedCategories.push(request.category);
        if (request.category === "subagent" && (call === 2 || call === 3)) throw new Error("refresh failed");
        return sessionPageFixture(request, [
          [
            request.category === "subagent" && call > 2
              ? "subagent-after-retry"
              : `${request.category}-before-refresh`,
          ],
        ]);
      },
    },
  ]);
  render(<AppRoot services={services} />);

  const browser = await screen.findByTestId("project-sessions-browser");
  fireEvent.click(requiredElement(within(browser).getAllByRole("tab"), 1));
  expect(await screen.findByText("subagent-before-refresh")).toBeInTheDocument();
  collectRefresh = true;

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
  fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
  expect(await screen.findByText("subagent-after-retry")).toBeInTheDocument();
});

function requiredElement(elements: readonly HTMLElement[], index: number): HTMLElement {
  const element = elements[index];
  if (element === undefined) throw new Error(`Required element ${index.toString()} is unavailable.`);
  return element;
}

import { act, render, waitFor } from "@testing-library/react";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { MemoryStorage } from "@/test-support/browser-storage";
import {
  parseSessionPageRequest,
  sessionPageFixture,
  sessionPageRequest,
  sessionSummaryFixture,
  type SessionPageRequest,
} from "@/test-support/session-catalog";
import { AppRoot } from "./AppRoot";

afterEach(() => {
  window.history.replaceState(null, "", "/");
  vi.unstubAllGlobals();
});

it("refetches only retained pages for the active Session category after reconnect", async () => {
  vi.stubGlobal("localStorage", new MemoryStorage());
  vi.stubGlobal("sessionStorage", new MemoryStorage());
  window.history.replaceState(null, "", "/projects/project-1");
  const requests: SessionPageRequest[] = [];
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
        const input = parseSessionPageRequest(params);
        requests.push(input);
        return sessionPageFixture({
          projectID: input.projectID,
          category: input.category,
          sessions: [
            sessionSummaryFixture(
              input.position.kind === "newest" ? "session-newest" : "session-older",
              input.category,
            ),
          ],
          ...(input.position.kind === "newest" ? { older: "older-1" } : {}),
        });
      },
    },
  ]);
  render(<AppRoot services={services} />);
  await waitFor(() => {
    expect(requests).toEqual([
      sessionPageRequest("project-1", "main", { kind: "newest" }),
      sessionPageRequest("project-1", "main", { kind: "older", token: "older-1" }),
    ]);
  });
  requests.length = 0;

  await act(async () => {
    services.transport.connection.set("disconnected");
    await Promise.resolve();
  });
  await act(async () => {
    services.transport.connection.set("connected");
    await Promise.resolve();
  });
  await waitFor(() => {
    expect(requests).toEqual([
      sessionPageRequest("project-1", "main", { kind: "newest" }),
      sessionPageRequest("project-1", "main", { kind: "older", token: "older-1" }),
    ]);
  });
});

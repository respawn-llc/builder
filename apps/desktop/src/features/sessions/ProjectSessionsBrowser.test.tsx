import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createTestServices, TestAppProviders, type TestAppServices } from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import {
  parseSessionPageRequest,
  sessionPageFixture,
  sessionPageRequest,
  sessionSummaryFixture,
} from "@/test-support/session-catalog";
import { getSelectedTabs, getTabs, getUnselectedTab } from "@/test-support/tabs";
import { ProjectSessionsBrowser } from "./ProjectSessionsBrowser";

describe("Project Sessions browser presentation", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps Sessions/Subagents controls visible through loading and requests only the selected category", async () => {
    const pending = new Promise<never>(() => undefined);
    const { services } = renderBrowser({
      method: "session.page",
      handler: async () => pending,
    });

    const browser = screen.getByTestId("project-sessions-browser");
    const tabs = getTabs(browser);
    expect(tabs).toHaveLength(2);
    expect(getSelectedTabs(browser)).toHaveLength(1);
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    expect(sessionRequests(services)).toEqual([sessionPageRequest("project-1", "main", { kind: "newest" })]);

    fireEvent.click(getUnselectedTab(browser));
    await waitFor(() => {
      expect(sessionRequests(services)).toEqual([
        sessionPageRequest("project-1", "main", { kind: "newest" }),
        sessionPageRequest("project-1", "subagent", { kind: "newest" }),
      ]);
    });
  });

  it("renders standard empty and retryable whole-list error states", async () => {
    const { view } = renderBrowser({
      method: "session.page",
      result: sessionPageFixture({
        projectID: "project-1",
        category: "main",
        sessions: [],
      }),
    });
    expect(await screen.findByTestId("empty-state")).toBeInTheDocument();
    view.unmount();

    const { services } = renderBrowser({
      method: "session.page",
      error: new Error("catalog failed"),
    });
    expect(await screen.findByTestId("error-state")).toBeInTheDocument();
    fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
    await waitFor(() => {
      expect(sessionRequests(services)).toHaveLength(2);
    });
  });

  it("projects compact deduplicated rows with fallback identity and no row interaction", async () => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-08-11T22:00:00Z"));
    renderBrowser({
      method: "session.page",
      result: sessionPageFixture({
        projectID: "project-1",
        category: "main",
        sessions: [
          sessionSummaryFixture("session-1", "main", {
            name: "  Named Session  ",
            preview: "  Preview copy  ",
            updatedAt: "2026-08-11T20:00:00Z",
          }),
          sessionSummaryFixture("session-1", "main", {
            name: "Older duplicate",
            preview: "Duplicate preview",
            updatedAt: "2026-08-11T19:00:00Z",
          }),
          sessionSummaryFixture("session-2", "main", {
            preview: "   ",
            updatedAt: "2026-08-11T21:00:00Z",
          }),
        ],
      }),
    });

    const rows = await screen.findAllByTestId("session-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("Named Session")).toBeInTheDocument();
    expect(screen.queryByText("Older duplicate")).not.toBeInTheDocument();
    expect(screen.getByText("Preview copy")).toBeInTheDocument();
    expect(screen.getByText("session-2")).toBeInTheDocument();
    const firstRow = rows[0];
    if (firstRow === undefined) throw new Error("Expected a rendered Session row.");
    expect(
      within(firstRow).getByText((content) => content.trim().length > 0, {
        selector: "time",
      }),
    ).toBeInTheDocument();
    for (const row of rows) {
      expect(within(row).queryByRole("button")).not.toBeInTheDocument();
      expect(within(row).queryByRole("link")).not.toBeInTheDocument();
      expect(row).not.toHaveAttribute("tabindex");
    }

    const destination = window.location.href;
    fireEvent.click(firstRow);
    expect(window.location.href).toBe(destination);
    await userEvent.tab();
    await userEvent.tab();
    for (const row of rows) {
      expect(row).not.toHaveFocus();
    }
  });

  it.each([
    {
      direction: "older" as const,
      boundaryTestID: "virtual-boundary-next",
      cursor: "older-1",
    },
    {
      direction: "newer" as const,
      boundaryTestID: "virtual-boundary-previous",
      cursor: "newer-1",
    },
  ])(
    "retains rows behind an independent $direction Retry boundary",
    async ({ boundaryTestID, cursor, direction }) => {
      renderBrowser({
        method: "session.page",
        handler: (params) => {
          const position = parseSessionPageRequest(params).position;
          if (position.kind === direction) throw new Error(`${direction} failed`);
          return sessionPageFixture({
            projectID: "project-1",
            category: "main",
            sessions: [sessionSummaryFixture("session-1", "main")],
            ...(direction === "older" ? { older: cursor } : {}),
            ...(direction === "newer" ? { newer: cursor } : {}),
          });
        },
      });

      expect(await screen.findByText("session-1")).toBeInTheDocument();
      expect(screen.getByTestId(boundaryTestID)).toBeInTheDocument();
      expect(screen.getByTestId("session-row")).toBeInTheDocument();
      expect(within(screen.getByTestId(boundaryTestID)).getByRole("button")).toBeInTheDocument();
    },
  );

  it("switches category content without retaining a hidden observer", async () => {
    const { services } = renderBrowser({
      method: "session.page",
      handler: (params) => {
        const category = parseSessionPageRequest(params).category;
        return sessionPageFixture({
          projectID: "project-1",
          category,
          sessions: [sessionSummaryFixture(`${category}-session`, category)],
        });
      },
    });
    expect(await screen.findByText("main-session")).toBeInTheDocument();

    fireEvent.click(getUnselectedTab(screen.getByTestId("project-sessions-browser")));
    expect(await screen.findByText("subagent-session")).toBeInTheDocument();
    expect(screen.queryByText("main-session")).not.toBeInTheDocument();
    expect(sessionRequests(services).map((entry) => entry.category)).toEqual(["main", "subagent"]);
  });
});

function renderBrowser(route: FakeRoute) {
  const services = createTestServices([route]);
  const view = render(
    <TestAppProviders services={services}>
      <ProjectSessionsBrowser projectID="project-1" />
    </TestAppProviders>,
  );
  return { services, view };
}

function sessionRequests(services: TestAppServices) {
  return services.transport.calls
    .filter((call) => call.method === "session.page")
    .map((call) => parseSessionPageRequest(call.params));
}

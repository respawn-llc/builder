import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useSyncExternalStore } from "react";

import { createTestServices, TestAppProviders, type TestAppServices } from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import {
  parseSessionPageRequest,
  sessionPageFixture,
  type SessionPageRequest,
} from "@/test-support/session-catalog";
import { ProjectSessionsBrowser } from "./ProjectSessionsBrowser";

const virtualizerHarness = vi.hoisted(() => {
  let itemCount = 0;
  let visibleIndexes: readonly number[] = [1];
  const listeners = new Set<() => void>();
  return {
    getSnapshot: () => visibleIndexes,
    itemCount: () => itemCount,
    setItemCount(count: number) {
      itemCount = count;
    },
    show(indexes: readonly number[]) {
      visibleIndexes = indexes;
      for (const listener of listeners) listener();
    },
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    virtualizer: {
      getOffsetForIndex: (index: number) => [index * 76],
      getTotalSize: () => 760,
      getVirtualItems: () =>
        visibleIndexes.map((index) => ({
          end: (index + 1) * 76,
          index,
          key: index.toString(),
          size: 76,
          start: index * 76,
        })),
      measureElement: vi.fn(),
      scrollToOffset: vi.fn(),
    },
  };
});

vi.mock("@tanstack/react-virtual", async (importOriginal) => ({
  ...(await importOriginal()),
  useVirtualizer: (options: Readonly<{ count: number }>) => {
    virtualizerHarness.setItemCount(options.count);
    useSyncExternalStore(
      virtualizerHarness.subscribe,
      virtualizerHarness.getSnapshot,
      virtualizerHarness.getSnapshot,
    );
    return virtualizerHarness.virtualizer;
  },
}));

describe("Project Sessions browser", () => {
  beforeEach(() => {
    virtualizerHarness.show([1]);
  });

  it("shows only the selected Project/category and moves one observer with the selection", async () => {
    const requests: SessionPageRequest[] = [];
    const route = sessionRoute((request) => {
      requests.push(request);
      return feedPage(request, [`${request.projectID}-${request.category}`]);
    });
    const view = renderBrowser(route, "project-1");

    expect(await screen.findByText("project-1-main")).toBeInTheDocument();
    const browser = screen.getByTestId("project-sessions-browser");
    expect(within(browser).getAllByRole("tab")).toHaveLength(2);

    fireEvent.click(within(browser).getByRole("tab", { selected: false }));
    expect(await screen.findByText("project-1-subagent")).toBeInTheDocument();

    view.rerender(browserProviders(view.services, "project-2"));
    expect(await screen.findByText("project-2-subagent")).toBeInTheDocument();
    expect(categories(requests, "project-1")).toEqual(new Set(["main", "subagent"]));
    expect(categories(requests, "project-2")).toEqual(new Set(["subagent"]));
  });

  it("renders loading, empty, and retryable whole-list failure states", async () => {
    const pending = new Promise<never>(() => undefined);
    let view = renderBrowser({ method: "session.page", handler: async () => pending });
    expect(await screen.findByTestId("loading-state")).toBeInTheDocument();
    view.unmount();

    virtualizerHarness.show([0]);
    view = renderBrowser({
      method: "session.page",
      result: sessionPageFixture({ projectID: "project-1", category: "main" }, []),
    });
    expect(await screen.findByTestId("empty-state")).toBeInTheDocument();
    view.unmount();

    view = renderBrowser({ method: "session.page", error: new Error("catalog failed") });
    expect(await screen.findByTestId("error-state")).toBeInTheDocument();
    fireEvent.click(within(screen.getByTestId("error-state-actions")).getByRole("button"));
    await waitFor(() => {
      expect(view.services.transport.calls.length).toBeGreaterThan(1);
    });
  });

  it("renders compact static rows with name, identity fallback, preview, and recency", async () => {
    virtualizerHarness.show([0, 1]);
    renderBrowser({
      method: "session.page",
      result: sessionPageFixture({ projectID: "project-1", category: "main" }, [
        ["session-1", "  Named Session  ", "  Preview copy  "],
        ["session-2"],
      ]),
    });

    const rows = await screen.findAllByTestId("session-row");
    expect(rows).toHaveLength(2);
    expect(screen.getByText("Named Session")).toBeInTheDocument();
    expect(screen.getByText("Preview copy")).toBeInTheDocument();
    expect(screen.getByText("session-2")).toBeInTheDocument();
    expect(screen.getAllByText((content) => content.trim().length > 0, { selector: "time" })).toHaveLength(2);
    for (const row of rows) {
      expect(within(row).queryByRole("button")).not.toBeInTheDocument();
      expect(within(row).queryByRole("link")).not.toBeInTheDocument();
      expect(row).not.toHaveAttribute("tabindex");
    }
  });

  it.each([
    ["virtual-boundary-next", "older"],
    ["virtual-boundary-previous", "newer"],
  ] as const)("retains rows behind the %s failure boundary", async (boundaryTestID, direction) => {
    let failNewer = false;
    renderBrowser(
      sessionRoute((request) => {
        if (direction === "older") {
          if (request.offset === 50) throw new Error("older failed");
          return feedPage(request, ["retained-session"], 50);
        }
        if (request.offset === 0 && failNewer) throw new Error("newer failed");
        return sessionNumberPage(request, request.offset / 50, request.offset + 50);
      }),
    );

    if (direction === "newer") {
      expect(await screen.findByText("session-0-1")).toBeInTheDocument();
      for (let page = 1; page <= 10; page += 1) {
        await loadPage("older", `session-${page.toString()}-1`);
      }
      failNewer = true;
    } else {
      expect(await screen.findByText("retained-session")).toBeInTheDocument();
    }
    const retainedIdentity = direction === "older" ? "retained-session" : "session-1-1";
    await reachEdge(direction);
    act(() => {
      virtualizerHarness.show(direction === "older" ? [1, 3] : [0, 2]);
    });
    const boundary = await screen.findByTestId(boundaryTestID);
    expect(screen.getByText(retainedIdentity)).toBeInTheDocument();
    expect(within(boundary).getByRole("button")).toBeInTheDocument();
  });

  it("shows one current occurrence when offset movement returns an older Session again", async () => {
    renderBrowser(
      sessionRoute((request) => {
        if (request.offset === 0) {
          return feedPage(request, ["moving-session", "Current occurrence"], 50);
        }
        const page = request.offset / 50;
        return feedPage(
          request,
          page === 1 ? ["moving-session", "Older occurrence"] : [`older-session-${page.toString()}`],
          request.offset + 50,
        );
      }),
    );

    expect(await screen.findByText("Current occurrence")).toBeInTheDocument();
    for (let page = 1; page <= 10; page += 1) {
      await loadPage("older", page === 1 ? "Current occurrence" : `older-session-${page.toString()}`);
    }
    act(() => {
      virtualizerHarness.show([1]);
    });
    expect(await screen.findByText("Older occurrence")).toBeInTheDocument();
    await loadPage("newer", "Current occurrence");
    expect(screen.queryByText("Older occurrence")).not.toBeInTheDocument();
    expect(screen.getAllByText("Current occurrence")).toHaveLength(1);
  });

  it("re-enters an evicted category at its changed current head", async () => {
    let currentHead = "head-before";
    renderBrowser(
      sessionRoute((request) => {
        if (request.category === "subagent") {
          return feedPage(request, ["subagent-head"]);
        }
        if (request.offset === 0) return feedPage(request, [currentHead], 50);
        const page = request.offset / 50;
        return feedPage(request, [`older-${page.toString()}`], request.offset + 50);
      }),
    );

    expect(await screen.findByText("head-before")).toBeInTheDocument();
    for (let page = 1; page <= 10; page += 1) {
      await loadPage("older", `older-${page.toString()}`);
    }
    expect(screen.queryByText("head-before")).not.toBeInTheDocument();
    const browser = screen.getByTestId("project-sessions-browser");
    fireEvent.click(within(browser).getByRole("tab", { selected: false }));
    expect(await screen.findByText("subagent-head")).toBeInTheDocument();

    currentHead = "head-after";
    fireEvent.click(within(browser).getByRole("tab", { selected: false }));
    expect(await screen.findByText("head-after")).toBeInTheDocument();
  });

  it("keeps traversing both directions beyond five retained pages at constant row count", async () => {
    const requests: SessionPageRequest[] = [];
    renderBrowser(
      sessionRoute((request) => {
        requests.push(request);
        const page = request.offset / 50;
        return sessionNumberPage(request, page, request.offset + 50);
      }),
    );

    expect(await screen.findByText("session-0-1")).toBeInTheDocument();
    for (let page = 1; page <= 10; page += 1) {
      await loadPage("older", `session-${page.toString()}-1`);
    }
    const retainedRowCount = screen.getAllByTestId("session-row").length;
    for (let page = 11; page <= 12; page += 1) {
      await loadPage("older", `session-${page.toString()}-1`);
      expect(screen.getAllByTestId("session-row")).toHaveLength(retainedRowCount);
    }
    expect(requests.map((request) => request.offset)).toContain(600);

    for (let page = 2; page >= 0; page -= 1) {
      await loadPage("newer", `session-${page.toString()}-1`);
      expect(screen.getAllByTestId("session-row")).toHaveLength(retainedRowCount);
    }
    expect(requests.map((request) => request.offset)).toContain(0);
  });
});

function renderBrowser(route: FakeRoute, projectID = "project-1") {
  const services = createTestServices([route]);
  const view = render(browserProviders(services, projectID));
  return { ...view, services };
}

function browserProviders(services: TestAppServices, projectID: string) {
  return (
    <TestAppProviders services={services}>
      <ProjectSessionsBrowser projectID={projectID} />
    </TestAppProviders>
  );
}

function categories(requests: readonly SessionPageRequest[], projectID: string) {
  return new Set(
    requests.filter((request) => request.projectID === projectID).map((request) => request.category),
  );
}

function sessionRoute(handler: (request: SessionPageRequest) => unknown): FakeRoute {
  return {
    method: "session.page",
    handler: (params) => handler(parseSessionPageRequest(params)),
  };
}

function sessionNumberPage(request: SessionPageRequest, page: number, nextOffset: number | null) {
  return sessionPageFixture(
    request,
    Array.from({ length: 3 }, (_, index) => [`session-${page.toString()}-${index.toString()}`] as const),
    nextOffset,
  );
}

type SessionFixture = Parameters<typeof sessionPageFixture>[1][number];

function feedPage(request: SessionPageRequest, session: SessionFixture, nextOffset: number | null = null) {
  const id = session[0];
  return sessionPageFixture(request, [[`${id}-filler-1`], session, [`${id}-filler-2`]], nextOffset);
}

async function loadPage(direction: "newer" | "older", visibleIdentity: string): Promise<void> {
  await reachEdge(direction);
  await waitFor(() => {
    const index = direction === "older" ? virtualizerHarness.itemCount() - 3 : 1;
    act(() => {
      virtualizerHarness.show([index]);
    });
    expect(screen.getByText(visibleIdentity)).toBeInTheDocument();
  });
  act(() => {
    virtualizerHarness.show([1]);
  });
}

async function reachEdge(direction: "newer" | "older"): Promise<void> {
  const itemCount = virtualizerHarness.itemCount();
  act(() => {
    virtualizerHarness.show([direction === "older" ? itemCount - 2 : 0]);
  });
  await act(async () => {
    await Promise.resolve();
  });
  act(() => {
    virtualizerHarness.show([1]);
  });
}

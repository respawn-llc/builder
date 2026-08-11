import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";

import { createTestServices, TestAppProviders, type TestAppServices } from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import {
  parseSessionPageRequest,
  sessionPageFixture,
  type SessionPageRequest,
} from "@/test-support/session-catalog";
import { InfiniteListBoundary, type VirtualizedInfiniteList } from "@/ui";
import { ProjectSessionsBrowser } from "./ProjectSessionsBrowser";

vi.mock("@/ui", async (importOriginal) => ({
  ...(await importOriginal()),
  VirtualizedInfiniteList: TestVirtualizedInfiniteList,
}));

describe("Project Sessions browser", () => {
  it("shows only the selected Project/category and moves one observer with the selection", async () => {
    const requests: SessionPageRequest[] = [];
    const route = sessionRoute((request) => {
      requests.push(request);
      return sessionPageFixture(request, [[`${request.projectID}-${request.category}`]]);
    });
    const view = renderBrowser(route, "project-1");

    expect(await screen.findByText("project-1-main")).toBeInTheDocument();
    const browser = screen.getByTestId("project-sessions-browser");
    const tabs = within(browser).getAllByRole("tab");
    expect(tabs).toHaveLength(2);
    expect(tabs.filter(selectedTab)).toHaveLength(1);

    fireEvent.click(requiredElement(tabs, 1));
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
    expect(
      within(requiredElement(rows, 0)).getByText((content) => content.trim().length > 0, {
        selector: "time",
      }),
    ).toBeInTheDocument();
    for (const row of rows) {
      expect(within(row).queryByRole("button")).not.toBeInTheDocument();
      expect(within(row).queryByRole("link")).not.toBeInTheDocument();
      expect(row).not.toHaveAttribute("tabindex");
    }
  });

  it.each([
    ["virtual-boundary-next", "older", "older-1"],
    ["virtual-boundary-previous", "newer", "newer-1"],
  ] as const)("retains rows behind the %s failure boundary", async (boundaryTestID, direction, cursor) => {
    renderBrowser(
      sessionRoute((request) => {
        if (request.position.kind === direction) throw new Error(`${direction} failed`);
        return sessionPageFixture(request, [["retained-session"]], {
          ...(direction === "older" ? { older: cursor } : { newer: cursor }),
        });
      }),
    );

    expect(await screen.findByText("retained-session")).toBeInTheDocument();
    fireEvent.click(await screen.findByTestId(`load-${direction}`));
    const boundary = await screen.findByTestId(boundaryTestID);
    expect(screen.getByText("retained-session")).toBeInTheDocument();
    expect(within(boundary).getByRole("button")).toBeInTheDocument();
  });

  it("shows one current occurrence when live cursor movement returns an older Session again", async () => {
    const olderPageByToken = numberedTokens("older", 5);
    renderBrowser(
      sessionRoute((request) => {
        if (request.position.kind === "newest") {
          return sessionPageFixture(request, [["head"]], { older: "older-1" });
        }
        if (request.position.kind === "newer") {
          return sessionPageFixture(request, [["moving-session", "Current occurrence"]], {
            older: "older-1",
          });
        }
        const number = requiredPage(olderPageByToken, request.position.token);
        return sessionPageFixture(
          request,
          [number === 1 ? ["moving-session", "Older occurrence"] : [`older-session-${number.toString()}`]],
          {
            ...(number < 5 ? { older: `older-${(number + 1).toString()}` } : {}),
            newer: `newer-${(number - 1).toString()}`,
          },
        );
      }),
    );

    for (let page = 1; page <= 5; page += 1) {
      await loadPage("older", page === 1 ? "Older occurrence" : `older-session-${page.toString()}`);
    }
    await loadPage("newer", "Current occurrence");
    expect(screen.queryByText("Older occurrence")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("session-row")).toHaveLength(4);
  });

  it("re-enters an evicted category at its changed current head", async () => {
    let currentHead = "head-before";
    const olderPageByToken = numberedTokens("older", 5);
    renderBrowser(
      sessionRoute((request) => {
        if (request.category === "subagent") {
          return sessionPageFixture(request, [["subagent-head"]]);
        }
        if (request.position.kind === "newest") {
          return sessionPageFixture(request, [[currentHead]], { older: "older-1" });
        }
        if (request.position.kind === "newer") {
          throw new Error("Re-entry fixture does not load toward newer.");
        }
        const number = requiredPage(olderPageByToken, request.position.token);
        return sessionPageFixture(request, [[`older-${number.toString()}`]], {
          ...(number < 5 ? { older: `older-${(number + 1).toString()}` } : {}),
          newer: `newer-${(number - 1).toString()}`,
        });
      }),
    );

    for (let page = 1; page <= 5; page += 1) {
      await loadPage("older", `older-${page.toString()}`);
    }
    expect(screen.queryByText("head-before")).not.toBeInTheDocument();
    const browser = screen.getByTestId("project-sessions-browser");
    fireEvent.click(requiredElement(within(browser).getAllByRole("tab"), 1));
    expect(await screen.findByText("subagent-head")).toBeInTheDocument();

    currentHead = "head-after";
    fireEvent.click(requiredElement(within(browser).getAllByRole("tab"), 0));
    expect(await screen.findByText("head-after")).toBeInTheDocument();
  });

  it("keeps traversing both directions beyond five retained pages at constant row count", async () => {
    const requests: SessionPageRequest[] = [];
    const olderPageByToken = numberedTokens("older", 7);
    const newerPageByToken = numberedTokens("newer", 6, 0);
    renderBrowser(
      sessionRoute((request) => {
        requests.push(request);
        if (request.position.kind === "newest") {
          return sessionNumberPage(request, 0, { older: "older-1" });
        }
        if (request.position.kind === "older") {
          const page = requiredPage(olderPageByToken, request.position.token);
          return sessionNumberPage(request, page, {
            ...(page < 7 ? { older: `older-${(page + 1).toString()}` } : {}),
            newer: `newer-${(page - 1).toString()}`,
          });
        }
        const page = requiredPage(newerPageByToken, request.position.token);
        return sessionNumberPage(request, page, {
          older: `older-${(page + 1).toString()}`,
          ...(page > 0 ? { newer: `newer-${(page - 1).toString()}` } : {}),
        });
      }),
    );

    expect(await screen.findByText("session-0-0")).toBeInTheDocument();
    for (let page = 1; page <= 5; page += 1) {
      await loadPage("older", `session-${page.toString()}-2`);
    }
    const retainedRowCount = screen.getAllByTestId("session-row").length;
    for (let page = 6; page <= 7; page += 1) {
      await loadPage("older", `session-${page.toString()}-2`);
      expect(screen.getAllByTestId("session-row")).toHaveLength(retainedRowCount);
    }
    expect(requests.map((request) => request.position)).toContainEqual({
      kind: "older",
      token: "older-7",
    });

    for (let page = 2; page >= 0; page -= 1) {
      await loadPage("newer", `session-${page.toString()}-0`);
      expect(screen.getAllByTestId("session-row")).toHaveLength(retainedRowCount);
    }
    expect(requests.map((request) => request.position)).toContainEqual({
      kind: "newer",
      token: "newer-0",
    });
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

function sessionNumberPage(
  request: SessionPageRequest,
  page: number,
  cursors: Readonly<{ older?: string | undefined; newer?: string | undefined }>,
) {
  return sessionPageFixture(
    request,
    Array.from({ length: 3 }, (_, index) => [`session-${page.toString()}-${index.toString()}`] as const),
    cursors,
  );
}

function numberedTokens(prefix: "newer" | "older", last: number, first = 1): ReadonlyMap<string, number> {
  return new Map(
    Array.from({ length: last - first + 1 }, (_, index) => {
      const page = first + index;
      return [`${prefix}-${page.toString()}`, page] as const;
    }),
  );
}

function requiredPage(pages: ReadonlyMap<string, number>, token: string): number {
  const page = pages.get(token);
  if (page === undefined) throw new Error("Fixture received an unknown Session cursor.");
  return page;
}

function selectedTab(tab: HTMLElement): boolean {
  return tab.getAttribute("aria-selected") === "true";
}

function requiredElement(elements: readonly HTMLElement[], index: number): HTMLElement {
  const element = elements[index];
  if (element === undefined) throw new Error(`Required element ${index.toString()} is unavailable.`);
  return element;
}

async function loadPage(direction: "newer" | "older", visibleIdentity: string): Promise<void> {
  const edge = await screen.findByTestId(`load-${direction}`);
  await waitFor(() => {
    expect(edge).toBeEnabled();
  });
  fireEvent.click(edge);
  expect(await screen.findByText(visibleIdentity)).toBeInTheDocument();
}

type VirtualizedInfiniteListProps<TItem> = Parameters<typeof VirtualizedInfiniteList<TItem>>[0];

function TestVirtualizedInfiniteList<TItem>(props: VirtualizedInfiniteListProps<TItem>) {
  const [lastOlderKey, setLastOlderKey] = useState<string>();
  const [lastNewerKey, setLastNewerKey] = useState<string>();
  const canLoadOlder =
    props.hasNextPage &&
    !props.isFetchingNextPage &&
    props.loadMoreKey !== undefined &&
    lastOlderKey !== props.loadMoreKey;
  const canLoadNewer =
    props.hasPreviousPage &&
    !props.isFetchingPreviousPage &&
    props.previousLoadKey !== undefined &&
    lastNewerKey !== props.previousLoadKey;
  return (
    <div>
      {props.previousBoundary && <InfiniteListBoundary direction="previous" state={props.previousBoundary} />}
      {props.items.length === 0
        ? props.empty
        : props.items.map((item, index) => (
            <div key={props.getItemKey(item)}>{props.renderItem(item, index)}</div>
          ))}
      {props.nextBoundary && <InfiniteListBoundary direction="next" state={props.nextBoundary} />}
      {props.hasPreviousPage ? (
        <button
          data-testid="load-newer"
          disabled={!canLoadNewer}
          onClick={() => {
            setLastNewerKey(props.previousLoadKey);
            props.onLoadPrevious?.();
          }}
        />
      ) : null}
      {props.hasNextPage ? (
        <button
          data-testid="load-older"
          disabled={!canLoadOlder}
          onClick={() => {
            setLastOlderKey(props.loadMoreKey);
            props.onLoadMore();
          }}
        />
      ) : null}
    </div>
  );
}

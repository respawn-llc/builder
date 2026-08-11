import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { z } from "zod";

import type { SessionCategory, SessionPagePosition } from "@/api";
import { queryKeys } from "@/app-facade";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import type { FakeRoute } from "@/test-support/api";
import { useProjectSessionsData } from "./useProjectSessionsData";

describe("Project Sessions query ownership", () => {
  it("projects an empty newest page as an empty ready collection", async () => {
    const harness = createHarness({
      method: "session.page",
      result: {
        project_id: "project-1",
        category: "main",
        sessions: [],
      },
    });
    const view = renderHook(
      () => useProjectSessionsData("project-1", "main"),
      { wrapper: harness.Wrapper },
    );

    await waitForReady(view.result);
    expect(ready(view.result.current).rows).toEqual([]);
  });

  it("requests only the selected Project/category and synchronously removes exited scopes", async () => {
    const requests: CatalogRequest[] = [];
    const harness = createHarness(catalogRoute(requests));
    const initialProps: Readonly<{ category: SessionCategory; projectID: string }> = {
      category: "main",
      projectID: "project-1",
    };
    const view = renderHook(
      ({ category, projectID }) => useProjectSessionsData(projectID, category),
      {
        initialProps,
        wrapper: harness.Wrapper,
      },
    );
    await waitForReady(view.result);

    view.rerender({ category: "subagent", projectID: "project-1" });
    await waitForReady(view.result);
    expect(
      harness.queryClient.getQueryData(
        queryKeys.projectSessionCatalog("project-1", "main"),
      ),
    ).toBeUndefined();

    view.rerender({ category: "subagent", projectID: "project-2" });
    await waitForReady(view.result);
    expect(
      harness.queryClient.getQueryData(
        queryKeys.projectSessionCatalog("project-1", "subagent"),
      ),
    ).toBeUndefined();
    expect(requests).toEqual([
      request("project-1", "main", { kind: "newest" }),
      request("project-1", "subagent", { kind: "newest" }),
      request("project-2", "subagent", { kind: "newest" }),
    ]);
  });

  it("deduplicates newest-first rows and keeps cursor keys advancing through five-page eviction", async () => {
    const requests: CatalogRequest[] = [];
    const harness = createHarness(pagedCatalogRoute(requests));
    const view = renderHook(
      () => useProjectSessionsData("project-1", "main"),
      { wrapper: harness.Wrapper },
    );
    await waitForReady(view.result);
    expect(ready(view.result.current).rows.map((row) => row.name)).toEqual([
      "Newest occurrence",
    ]);

    const olderKeys: string[] = [];
    for (let page = 1; page <= 10; page += 1) {
      olderKeys.push(required(ready(view.result.current).loadMoreKey));
      act(() => {
        ready(view.result.current).loadOlder();
      });
      await waitFor(() => {
        expect(requests).toContainEqual(
          request("project-1", "main", {
            kind: "older",
            token: `older-${String(page)}`,
          }),
        );
      });
      await waitForReady(view.result);
      if (page === 1) {
        expect(
          ready(view.result.current).rows.filter((row) => row.id === "session-shared"),
        ).toEqual([
          expect.objectContaining({ name: "Newest occurrence" }),
        ]);
      }
    }
    expect(new Set(olderKeys).size).toBe(10);
    const cached = harness.queryClient.getQueryData<{ pages: readonly unknown[] }>(
      queryKeys.projectSessionCatalog("project-1", "main"),
    );
    expect(cached?.pages).toHaveLength(5);
    const newerKeys: string[] = [];
    for (let page = 5; page >= 0; page -= 1) {
      newerKeys.push(required(ready(view.result.current).previousLoadKey));
      act(() => {
        ready(view.result.current).loadNewer();
      });
      await waitFor(() => {
        expect(requests.at(-1)).toEqual(
          request("project-1", "main", {
            kind: "newer",
            token: `newer-${String(page)}`,
          }),
        );
      });
      await waitForReady(view.result);
    }
    expect(new Set(newerKeys).size).toBe(6);
  });

  it("re-enters a deeply evicted category with exactly one newest request after rapid switching", async () => {
    const requests: CatalogRequest[] = [];
    const harness = createHarness(pagedCatalogRoute(requests));
    const initialProps: Readonly<{ category: SessionCategory }> = {
      category: "main",
    };
    const view = renderHook(
      ({ category }) => useProjectSessionsData("project-1", category),
      {
        initialProps,
        wrapper: harness.Wrapper,
      },
    );
    await waitForReady(view.result);
    for (let page = 1; page <= 5; page += 1) {
      act(() => {
        ready(view.result.current).loadOlder();
      });
      await waitFor(() => {
        expect(requests).toContainEqual(
          request("project-1", "main", {
            kind: "older",
            token: `older-${String(page)}`,
          }),
        );
      });
      await waitForReady(view.result);
    }

    const reentryStart = requests.length;
    view.rerender({ category: "subagent" });
    view.rerender({ category: "main" });
    await waitForReady(view.result);
    await waitFor(() => {
      expect(
        requests.slice(reentryStart).filter((entry) => entry.category === "main"),
      ).toEqual([request("project-1", "main", { kind: "newest" })]);
    });
    expect(
      harness.queryClient.getQueryData(
        queryKeys.projectSessionCatalog("project-1", "main"),
      ),
    ).toBeDefined();
  });

  it("removes its exact query on route unmount and remounts from newest", async () => {
    const requests: CatalogRequest[] = [];
    const harness = createHarness(catalogRoute(requests));
    const view = renderHook(
      () => useProjectSessionsData("project-1", "main"),
      { wrapper: harness.Wrapper },
    );
    await waitForReady(view.result);
    view.unmount();
    expect(
      harness.queryClient.getQueryData(
        queryKeys.projectSessionCatalog("project-1", "main"),
      ),
    ).toBeUndefined();
    const remountStart = requests.length;

    const utils = renderHook(
      () => useProjectSessionsData("project-1", "main"),
      { wrapper: harness.Wrapper },
    );
    await waitForReady(utils.result);
    expect(requests.slice(remountStart)).toEqual([
      request("project-1", "main", { kind: "newest" }),
    ]);
  });

  it("classifies initial, directional, and replacement failures without discarding directional rows", async () => {
    const initial = createHarness({
      method: "session.page",
      error: new Error("initial failed"),
    });
    {
      const view = renderHook(
        () => useProjectSessionsData("project-1", "main"),
        { wrapper: initial.Wrapper },
      );
      await waitFor(() => {
        expect(view.result.current.kind).toBe("error");
      });
    }

    const older = createHarness(failingDirectionRoute("older"));
    {
      const view = renderHook(
        () => useProjectSessionsData("project-1", "main"),
        { wrapper: older.Wrapper },
      );
      await waitForReady(view.result);
      act(() => {
        ready(view.result.current).loadOlder();
      });
      await waitFor(() => {
        expect(ready(view.result.current).olderFailed).toBe(true);
      });
      expect(ready(view.result.current).rows).toHaveLength(1);
    }

    const newer = createHarness(failingDirectionRoute("newer"));
    {
      const view = renderHook(
        () => useProjectSessionsData("project-1", "main"),
        { wrapper: newer.Wrapper },
      );
      await waitForReady(view.result);
      act(() => {
        ready(view.result.current).loadNewer();
      });
      await waitFor(() => {
        expect(ready(view.result.current).newerFailed).toBe(true);
      });
      expect(ready(view.result.current).rows).toHaveLength(1);
    }

    const replacement = createHarness({
      method: "session.page",
      handler: (_params, callIndex) => {
        if (callIndex > 0) throw new Error("refresh failed");
        return page({
          projectID: "project-1",
          category: "main",
          sessionID: "session-1",
          older: null,
          newer: null,
        });
      },
    });
    const view = renderHook(
      () => useProjectSessionsData("project-1", "main"),
      { wrapper: replacement.Wrapper },
    );
    await waitForReady(view.result);
    act(() => {
      view.result.current.retry();
    });
    await waitFor(() => {
      expect(view.result.current.kind).toBe("error");
    });
  });

});

type CatalogRequest = Readonly<{
  projectID: string;
  category: SessionCategory;
  position: SessionPagePosition;
}>;

function createHarness(route: FakeRoute) {
  const services = createTestServices([route]);
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false, staleTime: 0 },
    },
  });
  function Wrapper({ children }: Readonly<{ children: ReactNode }>) {
    return (
      <TestAppProviders queryClient={queryClient} services={services}>
        {children}
      </TestAppProviders>
    );
  }
  return { queryClient, services, Wrapper };
}

function catalogRoute(requests: CatalogRequest[]): FakeRoute {
  return {
    method: "session.page",
    handler: (params) => {
      const input = catalogRequest(params);
      requests.push(input);
      return page({
        projectID: input.projectID,
        category: input.category,
        sessionID: `${input.projectID}-${input.category}`,
        older: null,
        newer: null,
      });
    },
  };
}

function pagedCatalogRoute(requests: CatalogRequest[]): FakeRoute {
  const pageByToken = new Map<string, number>();
  for (let pageNumber = 0; pageNumber <= 20; pageNumber += 1) {
    pageByToken.set(`older-${String(pageNumber)}`, pageNumber);
    pageByToken.set(`newer-${String(pageNumber)}`, pageNumber);
  }
  return {
    method: "session.page",
    handler: (params) => {
      const input = catalogRequest(params);
      requests.push(input);
      if (input.position.kind === "newest") {
        return page({
          projectID: input.projectID,
          category: input.category,
          sessionID: "session-shared",
          older: "older-1",
          newer: null,
          name: "Newest occurrence",
        });
      }
      const pageNumber = pageByToken.get(input.position.token);
      if (pageNumber === undefined) {
        throw new Error("Fixture received an unknown Session cursor.");
      }
      if (input.position.kind === "older") {
        return page({
          projectID: input.projectID,
          category: input.category,
          sessionID: pageNumber === 1 ? "session-shared" : `session-${String(pageNumber)}`,
          older: `older-${String(pageNumber + 1)}`,
          newer: `newer-${String(pageNumber - 1)}`,
          name: pageNumber === 1 ? "Older occurrence" : `Session ${String(pageNumber)}`,
        });
      }
      return page({
        projectID: input.projectID,
        category: input.category,
        sessionID: `session-newer-${String(pageNumber)}`,
        older: `older-${String(pageNumber + 1)}`,
        newer: pageNumber > 0 ? `newer-${String(pageNumber - 1)}` : null,
      });
    },
  };
}

function failingDirectionRoute(direction: "older" | "newer"): FakeRoute {
  return {
    method: "session.page",
    handler: (params) => {
      const input = catalogRequest(params);
      if (input.position.kind === direction) {
        throw new Error(`${direction} failed`);
      }
      return page({
        projectID: input.projectID,
        category: input.category,
        sessionID: "session-1",
        older: "older-1",
        newer: "newer-1",
      });
    },
  };
}

function catalogRequest(params: unknown): CatalogRequest {
  const value = catalogRequestSchema.parse(params);
  return request(value.project_id, value.category, value.position);
}

const catalogRequestSchema = z.object({
  project_id: z.string(),
  category: z.enum(["main", "subagent"]),
  position: z.discriminatedUnion("kind", [
    z.object({ kind: z.literal("newest") }),
    z.object({ kind: z.literal("older"), token: z.string() }),
    z.object({ kind: z.literal("newer"), token: z.string() }),
  ]),
});

function request(
  projectID: string,
  category: SessionCategory,
  position: SessionPagePosition,
): CatalogRequest {
  return { projectID, category, position };
}

function page(input: Readonly<{
  projectID: string;
  category: SessionCategory;
  sessionID: string;
  older: string | null;
  newer: string | null;
  name?: string;
}>) {
  return {
    project_id: input.projectID,
    category: input.category,
    sessions: [
      {
        session_id: input.sessionID,
        category: input.category,
        name: input.name ?? "Session",
        first_prompt_preview: "Prompt",
        updated_at: "2026-08-11T20:00:00Z",
      },
    ],
    ...(input.older === null ? {} : { older: input.older }),
    ...(input.newer === null ? {} : { newer: input.newer }),
  };
}

function ready(value: ReturnType<typeof useProjectSessionsData>) {
  if (value.kind !== "ready") throw new Error(`Expected ready data, received ${value.kind}.`);
  return value;
}

async function waitForReady(
  result: Readonly<{ current: ReturnType<typeof useProjectSessionsData> }>,
) {
  await waitFor(() => {
    expect(result.current.kind).toBe("ready");
  });
}

function required(value: string | undefined): string {
  if (value === undefined) throw new Error("Expected a load suppression key.");
  return value;
}

import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ApiService, SessionCatalogPage, SessionCategory, WorkspaceList } from "@/api";
import {
  invalidateProjectSessionCatalogs,
  mainSessionCatalogInfiniteQueryOptions,
  subagentSessionCatalogInfiniteQueryOptions,
  workspaceCatalogInfiniteQueryOptions,
} from "./projectCatalogQueries";
import { queryKeys } from "./queryKeys";

describe("Project catalog query authority", () => {
  it("uses independent Project/category keys and a distinct workspace infinite-query key", () => {
    expect(queryKeys.projectSessionCatalog("project-1", "main")).not.toEqual(
      queryKeys.projectSessionCatalog("project-1", "subagent"),
    );
    expect(queryKeys.projectSessionCatalog("project-1", "main")).not.toEqual(
      queryKeys.projectSessionCatalog("project-2", "main"),
    );
    expect(queryKeys.projectWorkspaceCatalog("project-1")).not.toEqual(queryKeys.workspaces("project-1"));
  });

  it("retains ten Session pages and traverses from an evicted newest page back to offset zero", async () => {
    const requests: number[] = [];
    const api: Pick<ApiService, "listSessionPage"> = {
      listSessionPage: async (
        projectID: string,
        category: SessionCategory,
        offset: number,
      ): Promise<SessionCatalogPage> => {
        expect(projectID).toBe("project-1");
        expect(category).toBe("main");
        requests.push(offset);
        return {
          projectID,
          category,
          sessions: [],
          nextOffset: offset + 50,
        };
      },
    };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new InfiniteQueryObserver(
      queryClient,
      mainSessionCatalogInfiniteQueryOptions(api, "project-1"),
    );
    const unsubscribe = observer.subscribe(() => undefined);

    await waitForSuccess(observer);
    for (let index = 0; index < 10; index += 1) {
      await observer.fetchNextPage();
    }

    expect(requests).toEqual([0, 50, 100, 150, 200, 250, 300, 350, 400, 450, 500]);
    expect(observer.getCurrentResult().data?.pages).toHaveLength(10);
    expect(observer.getCurrentResult().data?.pageParams).toEqual([
      50, 100, 150, 200, 250, 300, 350, 400, 450, 500,
    ]);

    await observer.fetchPreviousPage();
    expect(requests.at(-1)).toBe(0);
    expect(observer.getCurrentResult().data?.pageParams[0]).toBe(0);
    expect(observer.getCurrentResult().data?.pages).toHaveLength(10);
    unsubscribe();
  });

  it("keeps workspace traversal forward-only and retains fewer than the full collection", async () => {
    const requests: (string | undefined)[] = [];
    const pageByToken = new Map<string, number>();
    const api: Pick<ApiService, "listWorkspaces"> = {
      listWorkspaces: async (projectID: string, pageToken?: string): Promise<WorkspaceList> => {
        expect(projectID).toBe("project-1");
        requests.push(pageToken);
        const page = pageToken === undefined ? 0 : pageByToken.get(pageToken);
        if (page === undefined) throw new Error("fixture received an unknown continuation.");
        const nextPageToken = `next-${String(page + 1)}`;
        pageByToken.set(nextPageToken, page + 1);
        return {
          projectID,
          workspaces: [],
          defaultWorkspaceID: "workspace-1",
          nextPageToken,
        };
      },
    };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new InfiniteQueryObserver(
      queryClient,
      workspaceCatalogInfiniteQueryOptions(api, "project-1"),
    );
    const unsubscribe = observer.subscribe(() => undefined);

    await waitForSuccess(observer);
    for (let index = 0; index < 5; index += 1) {
      await observer.fetchNextPage();
    }
    expect(requests).toEqual([undefined, "next-1", "next-2", "next-3", "next-4", "next-5"]);
    expect(observer.getCurrentResult().data?.pages).toHaveLength(4);
    expect(observer.getCurrentResult().hasPreviousPage).toBe(false);
    unsubscribe();
  });

  it("invalidates both Session categories under one Project root", async () => {
    const requests: SessionCategory[] = [];
    const api: Pick<ApiService, "listSessionPage"> = {
      listSessionPage: async (projectID: string, category: SessionCategory): Promise<SessionCatalogPage> => {
        requests.push(category);
        return { projectID, category, sessions: [], nextOffset: null };
      },
    };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const main = new InfiniteQueryObserver(
      queryClient,
      mainSessionCatalogInfiniteQueryOptions(api, "project-1"),
    );
    const subagent = new InfiniteQueryObserver(
      queryClient,
      subagentSessionCatalogInfiniteQueryOptions(api, "project-1"),
    );
    const unsubscribeMain = main.subscribe(() => undefined);
    const unsubscribeSubagent = subagent.subscribe(() => undefined);
    await Promise.all([waitForSuccess(main), waitForSuccess(subagent)]);
    requests.length = 0;

    await invalidateProjectSessionCatalogs(queryClient, "project-1");

    expect(requests).toEqual(["main", "subagent"]);
    expect(queryClient.getQueryData(queryKeys.projectSessionCatalog("project-2", "main"))).toBeUndefined();
    unsubscribeMain();
    unsubscribeSubagent();
  });
});

async function waitForSuccess(
  observer: Readonly<{ getCurrentResult(): Readonly<{ isSuccess: boolean }> }>,
): Promise<void> {
  await waitFor(() => {
    expect(observer.getCurrentResult().isSuccess).toBe(true);
  });
}

import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ProjectLabelCatalog } from "@/api";
import type { ProjectLabelDataContextValue } from "./projectLabelContext";
import { ProjectLabelDataContext } from "./projectLabelContext";
import { createProjectCatalogAuthority } from "./projectCatalogAuthority";
import { useProjectLabelCatalogMutations } from "./projectLabelHooks";
import { createLabelFilterState } from "./labelFilterState";

const api = vi.hoisted(() => ({
  reorderProjectLabels: vi.fn(),
}));

vi.mock("@/app-facade", () => {
  return {
    queryKeys: {
      projectLabels: (projectID: string) => ["project-labels", projectID],
    },
    useAppServices: () => ({ api }),
  };
});

const original: ProjectLabelCatalog = {
  projectID: "project-1",
  labels: [
    { id: "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf", name: "First" },
    { id: "b74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf", name: "Second" },
  ],
};

describe("project label mutations", () => {
  beforeEach(() => {
    api.reorderProjectLabels.mockReset();
  });

  it("refreshes again after a failed reorder instead of settling an older read", async () => {
    const oldRead = deferred<ProjectLabelCatalog>();
    const refreshed = deferred<ProjectLabelCatalog>();
    let readCount = 0;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const authority = createProjectCatalogAuthority({
      projectID: original.projectID,
      queryClient,
      listCatalog: async () => {
        readCount += 1;
        return readCount === 1 ? oldRead.promise : refreshed.promise;
      },
    });
    queryClient.setQueryData(["project-labels", original.projectID], original);
    const firstRead = queryClient
      .fetchQuery({
        queryKey: ["project-labels", original.projectID],
        queryFn: async ({ signal }) => authority.read(signal),
        staleTime: 0,
      })
      .catch(() => {
        return undefined;
      });
    await waitFor(() => {
      expect(readCount).toBe(1);
    });

    const failure = new Error("reorder failed");
    api.reorderProjectLabels.mockRejectedValueOnce(failure);
    const { result } = renderHook(() => useProjectLabelCatalogMutations(), {
      wrapper: createWrapper(queryClient, authority, original),
    });
    await act(async () => {
      await expect(
        result.current.reorder.mutateAsync(
          original.labels
            .slice()
            .reverse()
            .map((label) => label.id),
        ),
      ).rejects.toBe(failure);
    });

    expect(queryClient.getQueryData<ProjectLabelCatalog>(["project-labels", original.projectID])).toEqual(
      original,
    );
    oldRead.resolve(original);
    await firstRead;
    await waitFor(() => {
      expect(readCount).toBe(2);
    });

    const authoritative = {
      ...original,
      labels: [...original.labels].reverse(),
    };
    refreshed.resolve(authoritative);
    await waitFor(() => {
      expect(queryClient.getQueryData<ProjectLabelCatalog>(["project-labels", original.projectID])).toEqual(
        authoritative,
      );
    });
  });

  it("rejects a successful reorder response for another Project", async () => {
    const queryClient = new QueryClient();
    const authority = createProjectCatalogAuthority({
      projectID: original.projectID,
      queryClient,
      listCatalog: async () => original,
    });
    queryClient.setQueryData(["project-labels", original.projectID], original);
    api.reorderProjectLabels.mockResolvedValueOnce({
      projectID: "project-2",
      labels: original.labels,
    });
    const { result } = renderHook(() => useProjectLabelCatalogMutations(), {
      wrapper: createWrapper(queryClient, authority, original),
    });

    await act(async () => {
      await expect(
        result.current.reorder.mutateAsync(original.labels.map((label) => label.id)),
      ).rejects.toThrow("Project catalog authority received project-2 while serving project-1.");
    });
    expect(queryClient.getQueryData<ProjectLabelCatalog>(["project-labels", original.projectID])).toEqual(
      original,
    );
  });
});

function createWrapper(
  queryClient: QueryClient,
  authority: ReturnType<typeof createProjectCatalogAuthority>,
  catalog: ProjectLabelCatalog,
) {
  return function Wrapper({ children }: Readonly<{ children: ReactNode }>) {
    return createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(ContextProvider, { authority, catalog, children }),
    );
  };
}

function ContextProvider({
  authority,
  catalog,
  children,
}: Readonly<{
  authority: ReturnType<typeof createProjectCatalogAuthority>;
  catalog: ProjectLabelCatalog;
  children: ReactNode;
}>) {
  const query = useQuery({
    queryKey: ["project-labels", catalog.projectID],
    queryFn: async ({ signal }) => authority.read(signal),
    enabled: false,
  });
  const value: ProjectLabelDataContextValue = {
    authority,
    catalog: query,
    effects: {
      applyLocalCreate: vi.fn(),
      applyLocalDelete: vi.fn(),
      applyLocalReorder: async (nextCatalog, generation) => {
        authority.installCatalog(nextCatalog, generation);
      },
      applyLocalRename: vi.fn(),
      consumeProjectEvent: vi.fn(),
      refreshAfterSubscriptionBoundary: vi.fn(),
    },
    filter: {
      dispatch: vi.fn(),
      persistence: { status: "ready" },
      state: createLabelFilterState(),
    },
    projectID: catalog.projectID,
  };
  return createElement(ProjectLabelDataContext.Provider, { value }, children);
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return {
    promise,
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}

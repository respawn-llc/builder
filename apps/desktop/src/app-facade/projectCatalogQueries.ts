import {
  infiniteQueryOptions,
  queryOptions,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import type {
  ApiService,
  SessionCatalogPage,
  SessionCategory,
  WorkspaceCatalogPage,
} from "@/api";
import { sessionCatalogPageSize } from "@/api";
import { queryKeys } from "./queryKeys";

const sessionCatalogMaxPages = 10;
const workspaceCatalogMaxPages = 4;
type SessionCatalogApi = Pick<ApiService, "listSessionPage">;
type WorkspaceCatalogApi = Pick<ApiService, "listWorkspaces">;
type ProjectWorkspaceApi = Pick<ApiService, "getProjectWorkspace">;
type WorkspaceCatalogQueryKey = ReturnType<typeof queryKeys.projectWorkspaceCatalog>;

export function mainSessionCatalogInfiniteQueryOptions(api: SessionCatalogApi, projectID: string) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "main");
}

export function subagentSessionCatalogInfiniteQueryOptions(api: SessionCatalogApi, projectID: string) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "subagent");
}

export function workspaceCatalogInfiniteQueryOptions(api: WorkspaceCatalogApi, projectID: string) {
  return infiniteQueryOptions<
    WorkspaceCatalogPage,
    Error,
    InfiniteData<WorkspaceCatalogPage, number>,
    WorkspaceCatalogQueryKey,
    number
  >({
    queryKey: queryKeys.projectWorkspaceCatalog(projectID),
    queryFn: async ({ pageParam }) => api.listWorkspaces(projectID, pageParam),
    initialPageParam: 0,
    getNextPageParam: (lastPage: WorkspaceCatalogPage) => lastPage.nextOffset ?? undefined,
    getPreviousPageParam: (firstPage: WorkspaceCatalogPage) =>
      firstPage.offset === 0 ? undefined : Math.max(0, firstPage.offset - 100),
    maxPages: workspaceCatalogMaxPages,
  });
}

export function projectWorkspaceQueryOptions(
  api: ProjectWorkspaceApi,
  projectID: string,
  workspaceID: string | undefined,
) {
  return queryOptions({
    enabled: workspaceID !== undefined,
    queryKey:
      workspaceID === undefined
        ? [...queryKeys.projectWorkspaceCatalog(projectID), "initiating", null]
        : queryKeys.projectWorkspace(projectID, workspaceID),
    queryFn: async () => {
      if (workspaceID === undefined) {
        throw new Error("Initiating Workspace query requires a Workspace identity.");
      }
      return api.getProjectWorkspace(projectID, { workspaceID });
    },
    retry: false,
  });
}

export async function invalidateProjectSessionCatalogs(
  queryClient: QueryClient,
  projectID: string,
): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: queryKeys.projectSessionCatalogs(projectID),
    refetchType: "active",
  });
}

function sessionCatalogInfiniteQueryOptions(
  api: SessionCatalogApi,
  projectID: string,
  category: SessionCategory,
) {
  return infiniteQueryOptions({
    queryKey: queryKeys.projectSessionCatalog(projectID, category),
    queryFn: async ({ pageParam }) => api.listSessionPage(projectID, category, pageParam),
    initialPageParam: 0,
    getNextPageParam: (lastPage: SessionCatalogPage): number | undefined => lastPage.nextOffset ?? undefined,
    getPreviousPageParam: (
      _firstPage: SessionCatalogPage,
      _allPages: SessionCatalogPage[],
      firstPageParam: number,
    ): number | undefined =>
      firstPageParam === 0 ? undefined : Math.max(0, firstPageParam - sessionCatalogPageSize),
    maxPages: sessionCatalogMaxPages,
  });
}

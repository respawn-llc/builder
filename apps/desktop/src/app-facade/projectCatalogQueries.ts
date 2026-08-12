import { infiniteQueryOptions, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import type { ApiService, SessionCatalogPage, SessionCategory, WorkspaceList } from "@/api";
import { sessionCatalogPageSize } from "@/api";
import { queryKeys } from "./queryKeys";

const sessionCatalogMaxPages = 10;
const workspaceCatalogMaxPages = 4;
type SessionCatalogApi = Pick<ApiService, "listSessionPage">;
type WorkspaceCatalogApi = Pick<ApiService, "listWorkspaces">;
type SessionCatalogQueryKey = ReturnType<typeof queryKeys.projectSessionCatalog>;
type WorkspaceCatalogQueryKey = ReturnType<typeof queryKeys.projectWorkspaceCatalog>;

export function mainSessionCatalogInfiniteQueryOptions(api: SessionCatalogApi, projectID: string) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "main");
}

export function subagentSessionCatalogInfiniteQueryOptions(api: SessionCatalogApi, projectID: string) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "subagent");
}

export function workspaceCatalogInfiniteQueryOptions(api: WorkspaceCatalogApi, projectID: string) {
  return infiniteQueryOptions<
    WorkspaceList,
    Error,
    InfiniteData<WorkspaceList, string | null>,
    WorkspaceCatalogQueryKey,
    string | null
  >({
    queryKey: queryKeys.projectWorkspaceCatalog(projectID),
    queryFn: async ({ pageParam }) =>
      pageParam === null ? api.listWorkspaces(projectID) : api.listWorkspaces(projectID, pageParam),
    initialPageParam: null,
    getNextPageParam: (lastPage: WorkspaceList) => lastPage.nextPageToken ?? undefined,
    maxPages: workspaceCatalogMaxPages,
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
  return infiniteQueryOptions<
    SessionCatalogPage,
    Error,
    InfiniteData<SessionCatalogPage, number>,
    SessionCatalogQueryKey,
    number
  >({
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

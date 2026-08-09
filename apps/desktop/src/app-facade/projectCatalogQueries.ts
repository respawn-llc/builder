import { infiniteQueryOptions, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import type {
  ApiService,
  SessionCatalogPage,
  SessionCategory,
  SessionPagePosition,
  WorkspaceList,
} from "@/api";
import { queryKeys } from "./queryKeys";

const catalogMaxPages = 5;
type SessionCatalogApi = Pick<ApiService, "listSessionPage">;
type WorkspaceCatalogApi = Pick<ApiService, "listWorkspaces">;
type WorkspaceCatalogQueryKey = ReturnType<typeof queryKeys.projectWorkspaceCatalog>;

export function mainSessionCatalogInfiniteQueryOptions(
  api: SessionCatalogApi,
  projectID: string,
) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "main");
}

export function subagentSessionCatalogInfiniteQueryOptions(
  api: SessionCatalogApi,
  projectID: string,
) {
  return sessionCatalogInfiniteQueryOptions(api, projectID, "subagent");
}

export function workspaceCatalogInfiniteQueryOptions(
  api: WorkspaceCatalogApi,
  projectID: string,
) {
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
    maxPages: catalogMaxPages,
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
    initialPageParam: { kind: "newest" } satisfies SessionPagePosition,
    getNextPageParam: (lastPage: SessionCatalogPage): SessionPagePosition | undefined =>
      lastPage.older === null ? undefined : { kind: "older", token: lastPage.older },
    getPreviousPageParam: (firstPage: SessionCatalogPage): SessionPagePosition | undefined =>
      firstPage.newer === null ? undefined : { kind: "newer", token: firstPage.newer },
    maxPages: catalogMaxPages,
  });
}

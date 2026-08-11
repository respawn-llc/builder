import { infiniteQueryOptions, type InfiniteData } from "@tanstack/react-query";

import type { ApiService, ProjectEdit } from "@/api";
import { queryKeys } from "./queryKeys";

type ProjectEditApi = Pick<ApiService, "getProjectEdit">;
type ProjectEditQueryKey = ReturnType<typeof queryKeys.projectEdit>;

export function projectEditInfiniteQueryOptions(api: ProjectEditApi, projectID: string) {
  return infiniteQueryOptions<
    ProjectEdit,
    Error,
    InfiniteData<ProjectEdit, string>,
    ProjectEditQueryKey,
    string
  >({
    queryKey: queryKeys.projectEdit(projectID),
    queryFn: async ({ pageParam }) => api.getProjectEdit(projectID, pageParam),
    initialPageParam: "",
    enabled: projectID.length > 0,
    getNextPageParam: (lastPage) =>
      lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined,
  });
}

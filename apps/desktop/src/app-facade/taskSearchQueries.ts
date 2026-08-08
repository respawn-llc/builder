import type { QueryClient } from "@tanstack/react-query";

import { queryKeys } from "./queryKeys";

export async function invalidateProjectTaskSearches(
  queryClient: QueryClient,
  projectID: string,
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: queryKeys.projectTaskSearches(projectID),
      refetchType: "active",
    }),
    queryClient.invalidateQueries({
      queryKey: queryKeys.globalTaskSearches,
      refetchType: "active",
    }),
  ]);
}

export async function invalidateAllTaskSearches(queryClient: QueryClient): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: queryKeys.allTaskSearches,
    refetchType: "active",
  });
}

export async function removeProjectTaskSearches(queryClient: QueryClient, projectID: string): Promise<void> {
  queryClient.removeQueries({ queryKey: queryKeys.projectTaskSearches(projectID) });
  await queryClient.invalidateQueries({
    queryKey: queryKeys.globalTaskSearches,
    refetchType: "active",
  });
}

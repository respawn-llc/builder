import type { QueryClient } from "@tanstack/react-query";

import { queryKeys } from "./queryKeys";

export async function invalidateProjectTaskSearches(
  queryClient: QueryClient,
  projectID: string,
): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: queryKeys.projectTaskSearches(projectID),
    refetchType: "active",
  });
}

export async function invalidateAllTaskSearches(queryClient: QueryClient): Promise<void> {
  await queryClient.invalidateQueries({
    queryKey: queryKeys.allTaskSearches,
    refetchType: "active",
  });
}

export function removeProjectTaskSearches(queryClient: QueryClient, projectID: string): void {
  queryClient.removeQueries({ queryKey: queryKeys.projectTaskSearches(projectID) });
}

import type { QueryClient } from "@tanstack/react-query";

import { queryKeys } from "./queryKeys";
import { awaitAllQueryOperations } from "./queryErrors";

export async function invalidateProjectBoardQueries(
  queryClient: QueryClient,
  projectID: string,
): Promise<void> {
  await awaitAllQueryOperations([
    queryClient.invalidateQueries({
      queryKey: queryKeys.projectBoardsRoot(projectID),
      refetchType: "active",
    }),
    queryClient.invalidateQueries({
      queryKey: queryKeys.projectBoardNodeCardsRoot(projectID),
      refetchType: "active",
    }),
  ]);
}

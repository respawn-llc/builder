import { useInfiniteQuery } from "@tanstack/react-query";

import { queryKeys, useAppServices } from "@/app-facade";

export function useWorkflowPages(query = "", enabled = true) {
  const { api } = useAppServices();
  return useInfiniteQuery({
    queryKey: queryKeys.workflows(query),
    queryFn: async ({ pageParam }) => api.listWorkflows({ offset: pageParam, limit: 40, query }),
    initialPageParam: 0,
    getNextPageParam: (lastPage) => lastPage.nextOffset ?? undefined,
    enabled,
  });
}

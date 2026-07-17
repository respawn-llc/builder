import { useInfiniteQuery } from "@tanstack/react-query";

import { queryKeys, useAppServices } from "@/app-facade";

export function useWorkflowPages(query = "") {
  const { api } = useAppServices();
  return useInfiniteQuery({
    queryKey: queryKeys.workflows(query),
    queryFn: async ({ pageParam }) => api.listWorkflows({ pageToken: pageParam, query }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => (lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined),
  });
}

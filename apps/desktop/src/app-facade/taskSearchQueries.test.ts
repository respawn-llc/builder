import { QueryClient } from "@tanstack/react-query";

import { queryKeys } from "./queryKeys";
import {
  invalidateAllTaskSearches,
  invalidateProjectTaskSearches,
  removeProjectTaskSearches,
} from "./taskSearchQueries";

describe("Task Search query ownership", () => {
  it("invalidates one Project scope and the global scope", async () => {
    const queryClient = new QueryClient();
    const target = queryKeys.taskSearch("project-1", "search");
    const global = queryKeys.taskSearch(null, "search");
    const unrelated = queryKeys.taskSearch("project-2", "search");
    queryClient.setQueryData(target, {});
    queryClient.setQueryData(global, {});
    queryClient.setQueryData(unrelated, {});

    await invalidateProjectTaskSearches(queryClient, "project-1");

    expect(queryClient.getQueryState(target)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(global)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(unrelated)?.isInvalidated).toBe(false);
  });

  it("invalidates every Project scope after reconnect", async () => {
    const queryClient = new QueryClient();
    const first = queryKeys.taskSearch("project-1", "search");
    const second = queryKeys.taskSearch("project-2", "search");
    const global = queryKeys.taskSearch(null, "search");
    queryClient.setQueryData(first, {});
    queryClient.setQueryData(second, {});
    queryClient.setQueryData(global, {});

    await invalidateAllTaskSearches(queryClient);

    expect(queryClient.getQueryState(first)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(second)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(global)?.isInvalidated).toBe(true);
  });

  it("removes a deleted Project scope and invalidates global Search", async () => {
    const queryClient = new QueryClient();
    const deleted = queryKeys.taskSearch("project-1", "search");
    const retained = queryKeys.taskSearch("project-2", "search");
    const global = queryKeys.taskSearch(null, "search");
    queryClient.setQueryData(deleted, {});
    queryClient.setQueryData(retained, {});
    queryClient.setQueryData(global, {});

    await removeProjectTaskSearches(queryClient, "project-1");

    expect(queryClient.getQueryState(deleted)).toBeUndefined();
    expect(queryClient.getQueryState(retained)).toBeDefined();
    expect(queryClient.getQueryState(global)?.isInvalidated).toBe(true);
  });
});

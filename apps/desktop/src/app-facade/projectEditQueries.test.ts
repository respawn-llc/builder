import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";

import type { ApiService, ProjectEdit } from "@/api";
import { projectEditInfiniteQueryOptions } from "./projectEditQueries";
import { queryKeys } from "./queryKeys";

it("owns the unchanged Project Edit key, request, and pagination contract", async () => {
  const requests: (string | undefined)[] = [];
  const api: Pick<ApiService, "getProjectEdit"> = {
    getProjectEdit: async (projectID, pageToken): Promise<ProjectEdit> => {
      expect(projectID).toBe("project-1");
      requests.push(pageToken);
      return {
        projectID,
        projectKey: "KNT",
        displayName: "Kent",
        defaultWorkspaceID: "workspace-1",
        workspaces: [],
        nextPageToken: pageToken === "" ? "cursor-2" : "",
      };
    },
  };
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const observer = new InfiniteQueryObserver(
    queryClient,
    projectEditInfiniteQueryOptions(api, "project-1"),
  );
  const unsubscribe = observer.subscribe(() => {
    return undefined;
  });

  await waitFor(() => {
    expect(observer.getCurrentResult().isSuccess).toBe(true);
  });
  expect(observer.getCurrentResult().data?.pageParams).toEqual([""]);
  expect(observer.options.queryKey).toEqual(queryKeys.projectEdit("project-1"));
  await observer.fetchNextPage();

  expect(requests).toEqual(["", "cursor-2"]);
  unsubscribe();
});

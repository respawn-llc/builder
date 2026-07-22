import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import type { TaskLabelFilter } from "@/api";
import { AppServicesProvider, queryKeys } from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { useCreateTask } from "./useTaskMutations";

describe("task creation board membership refresh", () => {
  it.each([
    {
      name: "named",
      filter: {
        kind: "named",
        mode: "any",
        labelIDs: ["f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf"],
      } satisfies TaskLabelFilter,
    },
    {
      name: "unlabeled",
      filter: { kind: "unlabeled" } satisfies TaskLabelFilter,
    },
  ])("invalidates the active $name board and card membership after create", async ({ filter }) => {
    const services = createTestServices([
      {
        method: "workflow.task.create",
        result: { task: { id: "task-1" } },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: {
        mutations: { retry: false },
        queries: { retry: false },
      },
    });
    const boardKey = queryKeys.board("project-1", "workflow-1", filter);
    const cardsKey = queryKeys.boardNodeCards("project-1", "workflow-1", "backlog", filter);
    queryClient.setQueryData(boardKey, { projectID: "project-1" });
    queryClient.setQueryData(cardsKey, { pages: [], pageParams: [] });
    const { result } = renderHook(() => useCreateTask("project-1", "workflow-1", "workflow-1"), {
      wrapper: ({ children }: Readonly<{ children: ReactNode }>) => (
        <QueryClientProvider client={queryClient}>
          <AppServicesProvider services={services}>{children}</AppServicesProvider>
        </QueryClientProvider>
      ),
    });

    await act(async () => {
      await result.current.mutateAsync({
        body: "",
        labelIDs: filter.kind === "named" ? filter.labelIDs : [],
        projectID: "project-1",
        sourceWorkspaceID: "workspace-1",
        title: "Created task",
        workflowID: "workflow-1",
      });
    });

    expect(queryClient.getQueryState(boardKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(cardsKey)?.isInvalidated).toBe(true);
  });
});

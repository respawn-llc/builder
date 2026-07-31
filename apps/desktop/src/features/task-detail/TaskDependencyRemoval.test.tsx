import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { vi } from "vitest";

import type { TaskDetail } from "@/api";
import { AppServicesProvider, queryKeys } from "@/app-facade";
import { createTaskDetailTestServices, taskDetailResponse } from "@/test-support/task-detail";
import { useTaskMutations } from "./useTaskDetailData";

describe("Task dependency removal", () => {
  it("patches the open Task immediately and invalidates both Tasks plus project views", async () => {
    const services = createTaskDetailTestServices(taskWithBlocker(), {
      routes: [
        {
          method: "workflow.task.dependency.remove",
          result: {
            outcome: "removed",
            blocker_task_id: "task-2",
            blocker_short_id: "T-2",
            blocked_task_id: "task-1",
            blocked_short_id: "T-1",
          },
        },
      ],
    });
    const detail = await services.api.getTask("task-1");
    const queryClient = new QueryClient();
    queryClient.setQueryData(queryKeys.task("task-1"), detail);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useTaskMutations("task-1", "project-1"), {
      wrapper: testWrapper(services, queryClient),
    });

    await act(async () => {
      await result.current.removeDependency.mutateAsync({
        blockerTaskID: "task-2",
        blockedTaskID: "task-1",
      });
    });

    expect(queryClient.getQueryData<TaskDetail>(queryKeys.task("task-1"))?.dependencies.blockerCount).toBe(0);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.task("task-2"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.projectBoardsRoot("project-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.projectBoardNodeCardsRoot("project-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.projectTaskListsRoot("project-1"),
    });
  });

  it("requests an authoritative Task reload after failure", async () => {
    const services = createTaskDetailTestServices(taskWithBlocker(), {
      routes: [
        {
          method: "workflow.task.dependency.remove",
          error: new Error("offline"),
        },
      ],
    });
    const detail = await services.api.getTask("task-1");
    const queryClient = new QueryClient();
    queryClient.setQueryData(queryKeys.task("task-1"), detail);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useTaskMutations("task-1", "project-1"), {
      wrapper: testWrapper(services, queryClient),
    });

    await expect(
      result.current.removeDependency.mutateAsync({
        blockerTaskID: "task-2",
        blockedTaskID: "task-1",
      }),
    ).rejects.toThrow();

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: queryKeys.task("task-1"),
    });
  });
});

function testWrapper(services: ReturnType<typeof createTaskDetailTestServices>, queryClient: QueryClient) {
  return function TestWrapper({ children }: Readonly<{ children: ReactNode }>) {
    return (
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>{children}</AppServicesProvider>
      </QueryClientProvider>
    );
  };
}

function taskWithBlocker() {
  return {
    task: {
      ...taskDetailResponse.task,
      dependencies: {
        blocker_count: 1,
        unsatisfied_blocker_count: 1,
        directly_blocked_task_count: 0,
        directions: [
          {
            direction: "blocked-by",
            total_count: 1,
            unsatisfied_count: 1,
            items: [
              {
                task_id: "task-2",
                short_id: "T-2",
                title: "Prepare",
                workflow_id: "workflow-2",
                status: {
                  kind: "backlog",
                  native_state: "active",
                  node_ids: [],
                  attention_types: [],
                },
                satisfaction: "unsatisfied",
              },
            ],
            add_availability: { available: { remaining_capacity: 3 } },
          },
          {
            direction: "blocks",
            total_count: 0,
            items: [],
            add_availability: { available: { remaining_capacity: 2 } },
          },
        ],
      },
    },
  };
}

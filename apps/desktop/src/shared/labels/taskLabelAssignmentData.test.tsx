import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, renderHook, waitFor } from "@testing-library/react";
import { createElement, useEffect, type ReactNode } from "react";

import type { TaskDetail } from "@/api";
import { AppServicesProvider, queryKeys } from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { createTaskDetailFixture } from "@/test-support/task-detail";
import {
  createProjectCatalogAuthority,
  createProjectLabelEffects,
  TaskLabelAssignmentProvider,
  useTaskLabelAssignment,
  type TaskLabelAssignmentController,
} from "./index";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("task label assignment data", () => {
  it("loads the lightweight assignment and patches existing projections without a detail reload", async () => {
    const updateResponse = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => updateResponse.promise,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    queryClient.setQueryData(queryKeys.task("task-1"), await createTaskDetailFixture());
    queryClient.setQueryData(queryKeys.task("task-1"), await createTaskDetailFixture());
    queryClient.setQueryData(
      queryKeys.board("project-1", "workflow-1", {
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
      }),
      { projectID: "project-1" },
    );
    queryClient.setQueryData(
      queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", {
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
      }),
      { pages: [], pageParams: [] },
    );
    const taskListKey = [...queryKeys.allTaskLists, "project-1"];
    queryClient.setQueryData(taskListKey, { tasks: [] });
    const { result } = renderHook(() => useTaskLabelAssignment(), {
      wrapper: assignmentWrapper(services, queryClient, "task-1"),
    });
    await waitFor(() => {
      expect(result.current.controller).not.toBeNull();
    });
    const controller = result.current.controller;
    if (controller === null) {
      throw new Error("task label assignment controller did not initialize");
    }

    act(() => {
      controller.setDesired(priorityID, true);
    });

    expect(result.current.snapshot?.visibleLabelIDs).toEqual([priorityID]);
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.task.labels.update"),
    ).toHaveLength(1);
    expect(services.transport.calls.some((call) => call.method === "workflow.task.get")).toBe(false);

    updateResponse.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityID],
      },
    });
    await waitFor(() => {
      expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
        taskID: "task-1",
        labelIDs: [priorityID],
      });
      expect(queryClient.getQueryData<TaskDetail>(queryKeys.task("task-1"))?.labelIDs).toEqual([priorityID]);
    });
    expect(
      queryClient.getQueryCache().find({
        queryKey: queryKeys.board("project-1", "workflow-1", {
          kind: "named",
          mode: "any",
          labelIDs: [priorityID],
        }),
        exact: true,
      })?.state.isInvalidated,
    ).toBe(true);
    expect(
      queryClient.getQueryCache().find({
        queryKey: queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", {
          kind: "named",
          mode: "any",
          labelIDs: [priorityID],
        }),
        exact: true,
      })?.state.isInvalidated,
    ).toBe(true);
    expect(
      queryClient.getQueryCache().find({
        queryKey: taskListKey,
        exact: true,
      })?.state.isInvalidated,
    ).toBe(true);
  });

  it("shares one task-local controller and mutation lane across consumers", async () => {
    const updateResponse = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => updateResponse.promise,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const controllers: TaskLabelAssignmentController[] = [];
    render(
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(AppServicesProvider, {
          services,
          children: [
            createElement(TaskLabelAssignmentProvider, {
              catalog: projectLabelCatalog(),
              key: "first",
              taskID: "task-1",
              children: createElement(AssignmentControllerCapture, {
                onController: (controller) => {
                  controllers[0] = controller;
                },
              }),
            }),
            createElement(TaskLabelAssignmentProvider, {
              catalog: projectLabelCatalog(),
              key: "second",
              taskID: "task-1",
              children: createElement(AssignmentControllerCapture, {
                onController: (controller) => {
                  controllers[1] = controller;
                },
              }),
            }),
          ],
        }),
      ),
    );
    await waitFor(() => {
      expect(controllers).toHaveLength(2);
    });

    expect(controllers[0]).toBe(controllers[1]);
    act(() => {
      controllers[0]?.setDesired(priorityID, true);
      controllers[1]?.setDesired(priorityID, true);
    });

    expect(
      services.transport.calls.filter((call) => call.method === "workflow.task.labels.update"),
    ).toHaveLength(1);
  });

  it("retains rollback ownership after the last provider unmounts", async () => {
    const updateResponse = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => updateResponse.promise,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    queryClient.setQueryData(queryKeys.task("task-1"), await createTaskDetailFixture());
    const { result, unmount } = renderHook(() => useTaskLabelAssignment(), {
      wrapper: assignmentWrapper(services, queryClient, "task-1"),
    });
    await waitFor(() => {
      expect(result.current.controller).not.toBeNull();
    });

    act(() => {
      result.current.controller?.setDesired(priorityID, true);
    });
    expect(queryClient.getQueryData<TaskDetail>(queryKeys.task("task-1"))?.labelIDs).toEqual([priorityID]);

    unmount();
    updateResponse.reject(new Error("offline"));

    await waitFor(() => {
      expect(queryClient.getQueryData<TaskDetail>(queryKeys.task("task-1"))?.labelIDs).toEqual([]);
    });
  });

  it("defers a task labels-changed event until the local mutation lane drains", async () => {
    const updateResponse = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.labels.get",
        handler: (_params, callIndex) => ({
          assignment: {
            task_id: "task-1",
            label_ids: callIndex === 0 ? [] : [betaID],
          },
        }),
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => updateResponse.promise,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result } = renderHook(() => useTaskLabelAssignment(), {
      wrapper: assignmentWrapper(services, queryClient, "task-1"),
    });
    await waitFor(() => {
      expect(result.current.controller).not.toBeNull();
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.task.labels.get"),
      ).toHaveLength(1);
    });
    const controller = result.current.controller;
    if (controller === null) {
      throw new Error("task label assignment controller did not initialize");
    }
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => projectLabelCatalog(),
    });
    const effects = createProjectLabelEffects({
      authority,
      projectID: "project-1",
      queryClient,
    });

    act(() => {
      controller.setDesired(priorityID, true);
    });
    await act(async () => {
      await effects.consumeProjectEvent({
        action: "labels_changed",
        occurredAtUnixMs: 1,
        primaryEntityID: "task-1",
        projectID: "project-1",
        relatedIDs: [],
        resource: "task",
        workflowID: "workflow-1",
      });
    });

    expect(controller.getSnapshot().dirty).toBe(true);
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.task.labels.get"),
    ).toHaveLength(1);

    updateResponse.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityID],
      },
    });
    await waitFor(() => {
      expect(
        services.transport.calls.filter((call) => call.method === "workflow.task.labels.get"),
      ).toHaveLength(2);
      expect(controller.getSnapshot().visibleLabelIDs).toEqual([betaID]);
      expect(controller.getSnapshot().dirty).toBe(false);
    });
  });

  it("closes the task lane before a late assignment response after task deletion", async () => {
    const updateResponse = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => updateResponse.promise,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result } = renderHook(() => useTaskLabelAssignment(), {
      wrapper: assignmentWrapper(services, queryClient, "task-1"),
    });
    await waitFor(() => {
      expect(result.current.controller).not.toBeNull();
    });
    const controller = result.current.controller;
    if (controller === null) {
      throw new Error("task label assignment controller did not initialize");
    }
    const authority = createProjectCatalogAuthority({
      projectID: "project-1",
      queryClient,
      listCatalog: async () => projectLabelCatalog(),
    });
    const effects = createProjectLabelEffects({
      authority,
      projectID: "project-1",
      queryClient,
    });

    act(() => {
      controller.setDesired(priorityID, true);
    });
    await act(async () => {
      await effects.consumeProjectEvent({
        action: "deleted",
        occurredAtUnixMs: 1,
        primaryEntityID: "task-1",
        projectID: "project-1",
        relatedIDs: [],
        resource: "task",
        workflowID: "workflow-1",
      });
    });

    expect(controller.getSnapshot().closed).toBe(true);
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toBeUndefined();

    updateResponse.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityID],
      },
    });
    await waitFor(() => {
      expect(controller.getSnapshot().closed).toBe(true);
      expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toBeUndefined();
    });
  });
});

function AssignmentControllerCapture({
  onController,
}: Readonly<{
  onController(controller: TaskLabelAssignmentController): void;
}>) {
  const { controller } = useTaskLabelAssignment();
  useEffect(() => {
    if (controller !== null) {
      onController(controller);
    }
  }, [controller, onController]);
  return null;
}

function assignmentWrapper(
  services: ReturnType<typeof createTestServices>,
  queryClient: QueryClient,
  taskID: string,
) {
  return ({ children }: Readonly<{ children: ReactNode }>) =>
    createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(AppServicesProvider, {
        services,
        children: createElement(TaskLabelAssignmentProvider, {
          catalog: projectLabelCatalog(),
          taskID,
          children,
        }),
      }),
    );
}

function projectLabelCatalog() {
  return {
    projectID: "project-1",
    labels: [
      { id: priorityID, name: "Priority" },
      { id: betaID, name: "Beta" },
    ],
  };
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  let reject: ((error: unknown) => void) | null = null;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return {
    promise,
    reject(error: unknown): void {
      reject?.(error);
    },
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}

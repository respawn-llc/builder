import { ApiClient } from "./client";
import { taskDependenciesSchema, taskDependencyListResponseSchema } from "./schemas/workflowBoard";
import { FakeRpcTransport } from "@/test-support/api";

const status = {
  kind: "backlog",
  native_state: "active",
  node_ids: [],
  attention_types: [],
};

const dependencyItem = {
  task_id: "task-2",
  short_id: "KENT-2",
  title: "Prepare release",
  workflow_id: "workflow-2",
  status,
};

describe("task dependency client contract", () => {
  it("parses strict detail availability and server-owned blocker satisfaction", () => {
    const parsed = taskDependenciesSchema.parse({
      blocker_count: 1,
      unsatisfied_blocker_count: 1,
      directly_blocked_task_count: 0,
      directions: [
        {
          direction: "blocked-by",
          total_count: 1,
          unsatisfied_count: 1,
          items: [{ ...dependencyItem, satisfaction: "unsatisfied" }],
          add_availability: { available: { remaining_capacity: 49 } },
        },
        {
          direction: "blocks",
          total_count: 0,
          items: [],
          add_availability: { limit_reached: {} },
        },
      ],
    });

    expect(parsed).toMatchObject({
      blockerCount: 1,
      unsatisfiedBlockerCount: 1,
      directlyBlockedTaskCount: 0,
      directions: [
        {
          direction: "blocked-by",
          items: [{ satisfaction: "unsatisfied", status: { kind: "backlog" } }],
          addAvailability: { kind: "available", remainingCapacity: 49 },
        },
        {
          direction: "blocks",
          addAvailability: { kind: "limit_reached" },
        },
      ],
    });
  });

  it("rejects malformed direction fields, legacy availability, and inconsistent counts", () => {
    const base = {
      blocker_count: 0,
      unsatisfied_blocker_count: 0,
      directly_blocked_task_count: 0,
      directions: [
        {
          direction: "blocked-by",
          total_count: 0,
          unsatisfied_count: 0,
          items: [],
          add_availability: { available: { remaining_capacity: 1 } },
        },
        {
          direction: "blocks",
          total_count: 0,
          items: [],
          add_availability: { available: { remaining_capacity: 1 } },
        },
      ],
    };
    expect(() =>
      taskDependenciesSchema.parse({
        ...base,
        directions: [{ ...base.directions[0], unsatisfied_count: 1 }, base.directions[1]],
      }),
    ).toThrow();
    expect(() =>
      taskDependenciesSchema.parse({
        ...base,
        directions: [base.directions[0], { ...base.directions[1], unsatisfied_count: 0 }],
      }),
    ).toThrow();
    expect(() =>
      taskDependenciesSchema.parse({
        ...base,
        directions: [
          { ...base.directions[0], add_availability: { remaining_capacity: 1 } },
          base.directions[1],
        ],
      }),
    ).toThrow();
  });

  it("calls add, remove, and focused list using the locked methods and fields", async () => {
    const mutation = {
      outcome: "added",
      blocker_task_id: "task-1",
      blocker_short_id: "KENT-1",
      blocked_task_id: "task-2",
      blocked_short_id: "KENT-2",
    };
    const transport = new FakeRpcTransport([
      { method: "workflow.task.dependency.add", result: mutation },
      { method: "workflow.task.dependency.remove", result: { ...mutation, outcome: "removed" } },
      {
        method: "workflow.task.dependency.list",
        result: {
          task_id: "task-2",
          short_id: "KENT-2",
          directions: [
            {
              direction: "blocked-by",
              total_count: 1,
              unsatisfied_count: 1,
              items: [
                { ...dependencyItem, task_id: "task-1", short_id: "KENT-1", satisfaction: "unsatisfied" },
              ],
            },
          ],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.addTaskDependency("task-1", "task-2")).resolves.toMatchObject({
      outcome: "added",
      blockerTaskID: "task-1",
      blockedTaskID: "task-2",
    });
    await expect(client.removeTaskDependency("task-1", "task-2")).resolves.toMatchObject({
      outcome: "removed",
    });
    await expect(client.listTaskDependencies("task-2", "blocked-by")).resolves.toEqual(
      taskDependencyListResponseSchema.parse({
        task_id: "task-2",
        short_id: "KENT-2",
        directions: [
          {
            direction: "blocked-by",
            total_count: 1,
            unsatisfied_count: 1,
            items: [
              { ...dependencyItem, task_id: "task-1", short_id: "KENT-1", satisfaction: "unsatisfied" },
            ],
          },
        ],
      }),
    );

    expect(transport.calls).toEqual([
      {
        method: "workflow.task.dependency.add",
        params: { blocker_task_id: "task-1", blocked_task_id: "task-2" },
      },
      {
        method: "workflow.task.dependency.remove",
        params: { blocker_task_id: "task-1", blocked_task_id: "task-2" },
      },
      {
        method: "workflow.task.dependency.list",
        params: { task_id: "task-2", direction: "blocked-by" },
      },
    ]);
  });
});

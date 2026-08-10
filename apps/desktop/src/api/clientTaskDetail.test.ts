import { ContractError } from "./errors";
import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

describe("ApiClient Task Activity pagination", () => {
  it("uses offset pagination and rejects cursor-era responses", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.activity.list",
        result: {
          items: [
            {
              activity_id: "activity-1",
              type: "session_started",
              task_id: "task-1",
              occurred_at_unix_ms: 2,
              updated_at_unix_ms: 2,
              session_started: { session_id: "session-1", name: "Implementation" },
            },
          ],
          next_offset: 50,
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listTaskActivity("task-1", 0)).resolves.toMatchObject({
      items: [{ id: "activity-1", sessionID: "session-1" }],
      nextOffset: 50,
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.task.activity.list",
      params: { task_id: "task-1", offset: 0, limit: 50 },
    });

    const malformedClient = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.activity.list",
          result: { items: [], next_page_token: "legacy", generated_at_unix_ms: 1 },
        },
      ]),
    );
    await expect(malformedClient.listTaskActivity("task-1", 0)).rejects.toBeInstanceOf(ContractError);

    const mismatchedClient = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.activity.list",
          result: {
            items: [
              {
                activity_id: "activity-1",
                type: "session_started",
                task_id: "task-other",
                occurred_at_unix_ms: 2,
                updated_at_unix_ms: 2,
                session_started: { session_id: "session-1", name: "Implementation" },
              },
            ],
          },
        },
      ]),
    );
    await expect(mismatchedClient.listTaskActivity("task-1", 0)).rejects.toBeInstanceOf(ContractError);
  });
});

describe("ApiClient Task Comment pagination", () => {
  it("uses the paginated Task Comment RPC contract", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.comment.list",
        result: {
          items: [
            {
              id: "comment-1",
              task_id: "task-1",
              body: "Existing comment",
              author: "user",
              author_id: "Nek-12",
              created_at_unix_ms: 1,
              updated_at_unix_ms: 2,
            },
          ],
          next_offset: 40,
          total_count: 41,
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listTaskComments("task-1", 0)).resolves.toMatchObject({
      items: [{ id: "comment-1", body: "Existing comment", authorKind: "user", authorID: "Nek-12" }],
      nextOffset: 40,
      totalCount: 41,
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.task.comment.list",
      params: { task_id: "task-1", offset: 0, limit: 50 },
    });
  });

  it("rejects zero continuation offsets before feature code receives a page", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.task.comment.list", result: { items: [], next_offset: 0 } }]),
    );

    await expect(client.listTaskComments("task-1", 0)).rejects.toBeInstanceOf(ContractError);
  });
});

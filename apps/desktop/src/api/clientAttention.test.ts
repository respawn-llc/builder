import { expect, it } from "vitest";

import { ContractError } from "./errors";
import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

it("rejects task-attention and activity items outside the requested task", async () => {
  const transport = new FakeRpcTransport([
    {
      method: "workflow.task.attention.list",
      result: {
        generated_at_unix_ms: 1,
        items: [
          {
            id: "interrupted_run:run-1",
            kind: "interrupted_run",
            project_id: "project-1",
            workflow_id: "workflow-1",
            task_id: "task-other",
            task_short_id: "KNT-2",
            task_title: "Other task",
            run_id: "run-1",
            message: "Run interrupted",
            occurred_at_unix_ms: 1,
          },
        ],
      },
    },
    {
      method: "workflow.task.activity.list",
      result: {
        generated_at_unix_ms: 1,
        items: [
          {
            activity_id: "comment:comment-1",
            type: "comment",
            task_id: "task-other",
            occurred_at_unix_ms: 1,
            updated_at_unix_ms: 1,
            actor: "",
            summary: "Comment",
          },
        ],
        next_page_token: "",
      },
    },
  ]);
  const client = new ApiClient(transport);

  await expect(client.listTaskAttention("task-requested")).rejects.toBeInstanceOf(ContractError);
  await expect(client.listTaskActivity("task-requested", "")).rejects.toBeInstanceOf(ContractError);
});

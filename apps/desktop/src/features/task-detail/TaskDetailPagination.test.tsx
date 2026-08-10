import { act, screen, waitFor } from "@testing-library/react";

import { appI18n } from "@/i18n";
import {
  emptyTaskAttentionResponse,
  mountTaskDetailSurface,
  taskDetailResponse,
  taskUpdatedEvent,
} from "@/test-support/task-detail";

describe("Task Detail feed presentation", () => {
  it("renders the default empty Comments state from the shared test service", async () => {
    mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
    });

    expect(await screen.findByText(appI18n.t("task.noCommentsTitle"))).toBeInTheDocument();
  });

  it("renders adjacent repeated Comment identities as separate feed rows", async () => {
    mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      comments: {
        items: [
          {
            id: "comment-duplicate",
            task_id: "task-1",
            body: "First occurrence",
            author: "user",
            created_at_unix_ms: 2,
            updated_at_unix_ms: 2,
          },
          {
            id: "comment-duplicate",
            task_id: "task-1",
            body: "Second occurrence",
            author: "user",
            created_at_unix_ms: 1,
            updated_at_unix_ms: 1,
          },
        ],
        next_offset: null,
        total_count: 2,
      },
    });

    await waitFor(() => {
      expect(screen.getByText("First occurrence")).toBeInTheDocument();
      expect(screen.getByText("Second occurrence")).toBeInTheDocument();
    });
    expect(screen.getAllByRole("article")).toHaveLength(2);
  });

  it("uses the authoritative Comment total for the tab badge", async () => {
    mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      comments: {
        items: [
          {
            id: "comment-window-item",
            task_id: "task-1",
            body: "Only retained window item",
            author: "user",
            created_at_unix_ms: 2,
            updated_at_unix_ms: 2,
          },
        ],
        next_offset: null,
        total_count: 501,
      },
    });

    const commentsTab = await screen.findByRole("tab", {
      name: new RegExp(appI18n.t("task.comments")),
    });
    expect(commentsTab).toHaveTextContent("501");
  });

  it("refetches the retained newest page on a live update without clearing visible rows", async () => {
    let latestCommentBody = "Initial comment";
    const services = mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      comments: commentPage(latestCommentBody),
      routes: [
        {
          method: "workflow.task.comment.list",
          handler: (_params, callIndex) => ({
            ...commentPage(latestCommentBody),
            items: [
              {
                ...commentPage(latestCommentBody).items[0],
                body: callIndex === 0 ? "Initial comment" : latestCommentBody,
              },
            ],
          }),
        },
      ],
    });

    expect(await screen.findByText("Initial comment")).toBeInTheDocument();
    await waitFor(() => {
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    latestCommentBody = "Updated comment";
    act(() => {
      services.transport.emit("workflow.project", taskUpdatedEvent);
    });

    expect(screen.getByText("Initial comment")).toBeInTheDocument();
    await screen.findByText("Updated comment");
    expect(
      services.transport.calls
        .filter((call) => call.method === "workflow.task.comment.list")
        .map((call) => call.params),
    ).toEqual([
      { task_id: "task-1", offset: 0, limit: 50 },
      { task_id: "task-1", offset: 0, limit: 50 },
    ]);
  });
});

function commentPage(body: string) {
  return {
    items: [
      {
        id: "comment-live",
        task_id: "task-1",
        body,
        author: "user",
        created_at_unix_ms: 2,
        updated_at_unix_ms: 2,
      },
    ],
    next_offset: null,
    total_count: 1,
  };
}

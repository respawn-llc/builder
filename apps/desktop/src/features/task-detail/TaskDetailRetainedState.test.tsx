import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { SidebarDestination } from "@/app-facade";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import { createTestSidebarController } from "@/test-support/sidebar";
import {
  commentListResponse,
  emptyTaskAttentionResponse,
  mountTaskDetailSurface,
  taskDetailResponse,
} from "@/test-support/task-detail";

describe("Task Detail retained sidebar state", () => {
  it("layers retained unsaved interface state over refreshed Task data before capture", async () => {
    const pageNavigator = createTestSidebarNavigator();
    const retainedState = {
      descriptionPresentation: { editing: false, expanded: true },
      draft: { body: "Unsaved body", title: "Unsaved title" },
      editingComment: { body: "Unsaved edited comment", id: "comment-1" },
      newCommentBody: "Unsaved new comment",
      questionSelections: { "ask-1": ["not-retained"] },
      scrollOffsetPx: 240,
      selectedTab: "comments",
    };

    mountTaskDetailSurface(taskDetailResponse, {
      comments: commentListResponse,
      attention: emptyTaskAttentionResponse,
      initialFocus: { kind: "dependencies" },
      navigator: pageNavigator,
      retainedState,
    });

    expect(pageNavigator.registerCapture).not.toHaveBeenCalled();
    expect(await screen.findByDisplayValue("Unsaved title")).toBeInTheDocument();

    await waitFor(() => {
      expect(pageNavigator.registerCapture).toHaveBeenCalled();
    });
    const calls = vi.mocked(pageNavigator.registerCapture).mock.calls;
    const latest = calls.at(-1)?.[0];
    if (latest === undefined) throw new Error("Expected Task Detail retained-state capture.");
    expect(latest()).toEqual(
      expect.objectContaining({
        descriptionPresentation: { editing: false, expanded: true },
        draft: { body: "Unsaved body", title: "Unsaved title" },
        editingComment: { body: "Unsaved edited comment", id: "comment-1" },
        newCommentBody: "Unsaved new comment",
        selectedTab: "comments",
      }),
    );
    expect(latest()).not.toHaveProperty("questionSelections");
  });

  it("preserves overlay composition while opening a dependency Task", async () => {
    const pageNavigator = createTestSidebarNavigator();
    const onMutated = vi.fn();
    const blockedBy = taskDetailResponse.task.dependencies.directions[0];
    const blocks = taskDetailResponse.task.dependencies.directions[1];
    if (blockedBy === undefined || blocks === undefined) throw new Error("Expected dependency directions.");
    const item = {
      task_id: "task-2", short_id: "T-2", title: "Prepare", workflow_id: "workflow-2",
      status: { kind: "backlog", native_state: "active", node_ids: [], attention_types: [] },
      satisfaction: "unsatisfied",
    };
    const dependencies = {
      blocker_count: 1, unsatisfied_blocker_count: 1, directly_blocked_task_count: 0,
      directions: [{ ...blockedBy, total_count: 1, unsatisfied_count: 1, items: [item] }, blocks],
    };
    mountTaskDetailSurface(
      { task: { ...taskDetailResponse.task, dependencies } },
      { navigator: pageNavigator, onMutated, sidebarMode: "overlay" },
    );
    const user = userEvent.setup();

    await user.click(await screen.findByTestId("dependency-row-task-2"));
    expect(pageNavigator.push).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      onMutated,
      taskID: "task-2",
    });
  });

  it("opens related creation through the standalone Task Detail root owner", async () => {
    const opened: SidebarDestination[] = [];
    const root = createTestSidebarController((destination) => {
      opened.push(destination);
    });
    mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      openSidebar: root.open,
    });
    const user = userEvent.setup();

    await user.click(await screen.findByTestId("dependency-add-blocked-by"));

    expect(opened).toEqual([
      expect.objectContaining({
        kind: "newTask",
        pendingRelationship: { newTaskRole: "blocker", originTaskID: "task-1" },
      }),
    ]);
  });
});

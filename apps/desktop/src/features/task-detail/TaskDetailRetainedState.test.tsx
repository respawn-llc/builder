import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { z } from "zod";

import type { JsonValue } from "@/api";
import { appI18n } from "@/i18n";
import type { SidebarDestination } from "@/app-facade";
import { createTestSidebarController, createTestSidebarNavigator } from "@/test-support/sidebar";
import {
  activityResponse,
  commentListResponse,
  emptyTaskAttentionResponse,
  mountTaskDetailSurface,
  questionAttention,
  taskDetailResponse,
} from "@/test-support/task-detail";

describe("Task Detail retained sidebar state", () => {
  it("restores the selected Activity feed but opens back at the top after visiting another Task", async () => {
    const pageNavigator = createTestSidebarNavigator();
    const taskB = taskDetailWithID("task-2", "Dependency target");
    const services = mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      comments: commentListResponse,
      navigator: pageNavigator,
      routes: [
        {
          method: "workflow.task.get",
          handler: (params) => (taskIDFromParams(params) === "task-2" ? taskB : taskDetailResponse),
        },
        {
          method: "workflow.task.comment.list",
          handler: (params) =>
            taskIDFromParams(params) === "task-2"
              ? { items: [], next_offset: null, total_count: 0 }
              : commentListResponse,
        },
        {
          method: "workflow.task.activity.list",
          handler: (params) => activityPage(taskIDFromParams(params)),
        },
      ],
    });
    const user = userEvent.setup();

    const activityTab = await screen.findByRole("tab", { name: appI18n.t("task.activity") });
    await user.click(activityTab);
    await waitFor(() => {
      expect(activityTab).toHaveAttribute("aria-selected", "true");
    });
    const list = await screen.findByTestId("task-detail-island-stack");
    list.scrollTop = 1200;
    fireEvent.scroll(list);

    await waitFor(() => {
      expect(pageNavigator.registerCapture).toHaveBeenCalled();
    });
    const capture = vi.mocked(pageNavigator.registerCapture).mock.lastCall?.[0];
    if (capture === undefined) throw new Error("Expected Task Detail retained-state capture.");
    const retainedState = capture();
    expect(retainedState).toEqual(
      expect.objectContaining({
        selectedTab: "activity",
      }),
    );
    expect(retainedState).not.toHaveProperty("scrollOffsetPx");

    services.rerenderTaskDetail("task-2");
    await screen.findByDisplayValue("Dependency target");

    services.rerenderTaskDetail("task-1", retainedState);
    const restoredActivityTab = await screen.findByRole("tab", {
      name: appI18n.t("task.activity"),
    });
    await waitFor(() => {
      expect(restoredActivityTab).toHaveAttribute("aria-selected", "true");
      expect(screen.getByTestId("task-detail-island-stack").scrollTop).toBe(0);
    });
  });

  it("lets an attention deep-link override retained Task Detail state", async () => {
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    });

    mountTaskDetailSurface(taskDetailResponse, {
      attention: { generated_at_unix_ms: 3, items: [questionAttention] },
      initialFocus: { kind: "question", askIDs: ["ask-1"] },
      retainedState: {
        base: { body: "Need operator input", title: "Resolve blocker" },
        descriptionPresentation: { editing: false, expanded: false },
        draft: { body: "Need operator input", title: "Resolve blocker" },
        editingComment: null,
        newCommentBody: "",
        selectedTab: "comments",
      },
    });

    await screen.findByText("Choose snack");
    await waitFor(() => {
      expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "auto", block: "start" });
    });
  });

  it("layers retained unsaved interface state over refreshed Task data before capture", async () => {
    const pageNavigator = createTestSidebarNavigator();
    const retainedState = {
      base: { body: "Need operator input", title: "Resolve blocker" },
      descriptionPresentation: { editing: false, expanded: true },
      draft: { body: "Unsaved body", title: "Unsaved title" },
      editingComment: { body: "Unsaved edited comment", id: "comment-1" },
      newCommentBody: "Unsaved new comment",
      questionSelections: { "ask-1": ["not-retained"] },
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
        base: { body: "Need operator input", title: "Resolve blocker" },
        descriptionPresentation: { editing: false, expanded: true },
        draft: { body: "Unsaved body", title: "Unsaved title" },
        editingComment: { body: "Unsaved edited comment", id: "comment-1" },
        newCommentBody: "Unsaved new comment",
        selectedTab: "comments",
      }),
    );
    expect(latest()).not.toHaveProperty("questionSelections");
  });

  it("lets refreshed server data replace a previously clean draft", async () => {
    mountTaskDetailSurface(taskDetailResponse, {
      attention: emptyTaskAttentionResponse,
      retainedState: {
        base: { body: "Old body", title: "Old title" },
        descriptionPresentation: { editing: false, expanded: false },
        draft: { body: "Old body", title: "Old title" },
        editingComment: null,
        newCommentBody: "",
        selectedTab: "comments",
      },
    });

    expect(await screen.findByDisplayValue("Resolve blocker")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("Old title")).not.toBeInTheDocument();
  });

  it("preserves overlay composition without carrying the current Task callback to a dependency", async () => {
    const pageNavigator = createTestSidebarNavigator();
    const onMutated = vi.fn();
    const blockedBy = taskDetailResponse.task.dependencies.directions[0];
    const blocks = taskDetailResponse.task.dependencies.directions[1];
    if (blockedBy === undefined || blocks === undefined) throw new Error("Expected dependency directions.");
    const item = {
      task_id: "task-2",
      short_id: "T-2",
      title: "Prepare",
      workflow_id: "workflow-2",
      status: { kind: "backlog", native_state: "active", node_ids: [], attention_types: [] },
      satisfaction: "unsatisfied",
    };
    const dependencies = {
      blocker_count: 1,
      unsatisfied_blocker_count: 1,
      directly_blocked_task_count: 0,
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
      taskID: "task-2",
    });
  });

  it("ignores malformed retained state and preserves first-open focus", async () => {
    const navigator = createTestSidebarNavigator();
    mountTaskDetailSurface(taskDetailResponse, {
      initialFocus: { kind: "dependencies" },
      navigator,
      retainedState: { selectedTab: "unknown" },
    });
    expect(await screen.findByDisplayValue("Resolve blocker")).toBeInTheDocument();
    await waitFor(() => {
      expect(navigator.registerCapture).toHaveBeenCalled();
    });
    const capture = vi.mocked(navigator.registerCapture).mock.lastCall?.[0];
    if (capture === undefined) throw new Error("Expected fallback-state capture.");
    expect(capture()).toEqual(
      expect.objectContaining({
        draft: { body: "Need operator input", title: "Resolve blocker" },
        selectedTab: "comments",
      }),
    );
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
    await user.click(await screen.findByRole("button", { name: appI18n.t("task.dependenciesCreateTask") }));

    expect(opened).toEqual([
      expect.objectContaining({
        kind: "newTask",
        pendingRelationship: { newTaskRole: "blocker", originTaskID: "task-1" },
      }),
    ]);
  });
});

function taskDetailWithID(taskID: string, title: string): JsonValue {
  return {
    task: {
      ...taskDetailResponse.task,
      summary: {
        ...taskDetailResponse.task.summary,
        id: taskID,
        short_id: taskID === "task-2" ? "T-2" : taskDetailResponse.task.summary.short_id,
        title,
      },
    },
  };
}

function taskIDFromParams(params: JsonValue): string {
  const result = z.object({ task_id: z.string() }).safeParse(params);
  if (!result.success) {
    throw new Error("Expected a Task-scoped RPC request.");
  }
  return result.data.task_id;
}

function activityPage(taskID: string) {
  const baseActivity = activityResponse.items[0];
  if (baseActivity === undefined) {
    throw new Error("Expected an Activity fixture.");
  }
  return {
    items: Array.from({ length: 50 }, (_value, index) => ({
      ...baseActivity,
      activity_id: `activity-${taskID}-${index.toString()}`,
      task_id: taskID,
      occurred_at_unix_ms: 1000 - index,
      updated_at_unix_ms: 1000 - index,
      comment: {
        ...baseActivity.comment,
        id: `comment-activity-${taskID}-${index.toString()}`,
        task_id: taskID,
        body: `Activity item ${index.toString()}`,
        created_at_unix_ms: 1000 - index,
        updated_at_unix_ms: 1000 - index,
      },
    })),
    next_offset: null,
  };
}

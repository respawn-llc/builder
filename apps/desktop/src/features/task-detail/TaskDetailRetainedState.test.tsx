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

  it("ignores an invalid retained snapshot and preserves the first-open focus request", async () => {
    const pageNavigator = createTestSidebarNavigator();
    mountTaskDetailSurface(taskDetailResponse, {
      initialFocus: { kind: "dependencies" },
      navigator: pageNavigator,
      retainedState: { scrollOffsetPx: -1 },
    });

    expect(await screen.findByDisplayValue("Resolve blocker")).toBeInTheDocument();
    await waitFor(() => {
      expect(pageNavigator.registerCapture).toHaveBeenCalled();
    });
    const latest = vi.mocked(pageNavigator.registerCapture).mock.calls.at(-1)?.[0];
    if (latest === undefined) throw new Error("Expected Task Detail fallback-state capture.");
    expect(latest()).toEqual(
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

    expect(opened).toEqual([
      expect.objectContaining({
        kind: "newTask",
        pendingRelationship: { newTaskRole: "blocker", originTaskID: "task-1" },
      }),
    ]);
  });
});

import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeFileTarget,
  type NativeNotificationActivation,
} from "@app/native-bridge";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { vi } from "vitest";

import { App } from "../../App";
import { guiTaskCommentAuthor } from "../../api/client";
import type { JsonObject, JsonValue } from "../../api/json";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";
import { showStatusToast, type StatusNotice } from "../../ui";
import type * as uiModule from "../../ui";

const statusToastHarness = vi.hoisted(() => ({
  notices: new Map<string, StatusNotice>(),
}));

vi.mock("../../ui", async (importOriginal) => {
  const actual = await importOriginal<typeof uiModule>();
  return {
    ...actual,
    dismissStatusToast: vi.fn((id: string) => {
      statusToastHarness.notices.delete(id);
    }),
    showStatusToast: vi.fn((notice: StatusNotice) => {
      statusToastHarness.notices.set(notice.id, notice);
    }),
    Toaster: () => null,
  };
});

describe("TaskDetailSurface", () => {
  beforeEach(() => {
    statusToastHarness.notices.clear();
    vi.mocked(showStatusToast).mockClear();
  });

  it("renders direct task route inline with inbox, comments, approvals, questions, and CLI actions", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const copied: string[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: taskDetailResponseWithNewerActiveRun },
        { method: "workflow.task.comment.list", result: commentListResponse },
        { method: "workflow.task.activity.list", result: activityResponse },
        { method: "ask.listPendingBySession", result: pendingAskResponse },
        { method: "workflow.task.question.answer", result: {} },
        { method: "workflow.task.approve", result: {} },
        { method: "workflow.task.comment.add", result: commentAddResponse },
        { method: "workflow.task.comment.replace", result: {} },
      ],
      nativeBridgeWithClipboard(copied),
    );

    render(<App services={services} />);

    await screen.findByRole("textbox", { name: "Description" });
    expect(screen.queryByTestId("task-detail-description-island")).not.toBeInTheDocument();

    const question = await screen.findByRole("region", { name: "Question" });
    expect(screen.queryByRole("region", { name: "Inbox" })).not.toBeInTheDocument();
    expect(screen.queryByText("Answer")).not.toBeInTheDocument();
    expect(within(question).queryByRole("heading", { name: "Question" })).not.toBeInTheDocument();
    const recommendedOption = await within(question).findByRole("radio", { name: /Use option A/u });
    expect(recommendedOption).toBeChecked();
    expect(within(question).getByRole("radio", { name: "Neither" })).toBeInTheDocument();
    expect(within(question).getByRole("textbox", { name: "Commentary" })).toBeInTheDocument();
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.ask_id).toBe("ask-1");
      expect(params.client_request_id).toMatch(/^gui-question-ask-1-/u);
      expect(params.freeform_answer).toBe("");
      expect(params.run_id).toBe("run-1");
      expect(params.selected_option_number).toBe(1);
      expect(params.task_id).toBe("task-1");
    });
    await waitFor(() => {
      expect(within(question).getByRole("radio", { name: /Use option A/u })).toBeDisabled();
      expect(within(question).getByRole("button", { name: "Submit answer" })).toBeDisabled();
    });

    expect(screen.queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.approve",
        params: { task_transition_id: "transition-1" },
      });
    });

    expect(screen.getAllByLabelText("Add comment")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Add comment"), {
      target: { value: "Fresh comment" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit comment" }));
    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.comment.add",
        params: { author: guiTaskCommentAuthor, body: "Fresh comment", task_id: "task-1" },
      });
    });

    fireEvent.click(await screen.findByRole("button", { name: /^Edit comment/ }));
    expect(screen.getAllByLabelText("Edit comment")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Edit comment"), {
      target: { value: "Edited comment" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save comment" }));
    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.comment.replace",
        params: { body: "Edited comment", comment_id: "comment-1" },
      });
    });

    fireEvent.click(screen.getByRole("button", { name: "Open in CLI" }));
    await waitFor(() => {
      expect(copied).toEqual(["kent --session=session-2"]);
    });
  });

  it("opens script files through native file capabilities without exposing CLI sessions", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const opened: NativeFileTarget[] = [];
    const checked: NativeFileTarget[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: taskDetailResponseWithScriptRun },
        { method: "workflow.task.comment.list", result: commentListResponse },
        { method: "workflow.task.activity.list", result: activityResponse },
      ],
      nativeBridgeWithFiles({ available: true, checked, opened }),
    );

    render(<App services={services} />);

    expect(await screen.findByRole("textbox", { name: "Description" })).toBeInTheDocument();
    const openScript = await screen.findByRole("button", { name: "Open script" });
    expect(screen.queryByRole("button", { name: "Open in CLI" })).not.toBeInTheDocument();

    fireEvent.click(openScript);

    await waitFor(() => {
      expect(checked).toContainEqual({ basePath: "/tmp/worktree", path: "scripts/run" });
      expect(opened).toEqual([{ basePath: "/tmp/worktree", path: "scripts/run" }]);
    });
  });

  it("hides script file opening when the native client cannot access the file", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const checked: NativeFileTarget[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: taskDetailResponseWithScriptRun },
        { method: "workflow.task.comment.list", result: commentListResponse },
        { method: "workflow.task.activity.list", result: activityResponse },
      ],
      nativeBridgeWithFiles({ available: false, checked, opened: [] }),
    );

    render(<App services={services} />);

    expect(await screen.findByRole("textbox", { name: "Description" })).toBeInTheDocument();
    await waitFor(() => {
      expect(checked).toContainEqual({ basePath: "/tmp/worktree", path: "scripts/run" });
      expect(screen.queryByRole("button", { name: "Open script" })).not.toBeInTheDocument();
    });
  });

  it("copies interrupted run structured details without rendering them inline", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const copied: string[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: taskDetailResponseWithInterruptedScriptRun },
        { method: "workflow.task.comment.list", result: commentListResponse },
        { method: "workflow.task.activity.list", result: activityResponse },
      ],
      nativeBridgeWithClipboard(copied),
    );

    render(<App services={services} />);

    const interrupted = await screen.findByRole("region", { name: "Interrupted" });
    expect(within(interrupted).getByText("Script failed")).toBeInTheDocument();
    expect(within(interrupted).queryByText(/permission denied/u)).not.toBeInTheDocument();

    fireEvent.click(within(interrupted).getByRole("button", { name: "Copy interruption details" }));

    await waitFor(() => {
      expect(copied).toEqual(['{"kind":"script_failure","stderr":"permission denied"}']);
    });
  });

  it("surfaces failed comment saves through the status toast surface", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.comment.list", result: commentListResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.comment.add", error: new Error("constraint failed") },
    ]);

    render(<App services={services} />);

    await screen.findByRole("textbox", { name: "Add comment" });
    fireEvent.change(screen.getByRole("textbox", { name: "Add comment" }), {
      target: { value: "Fresh comment" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit comment" }));

    await waitFor(() => {
      expect(toastCount()).toBe(1);
    });
  });

  it("submits a comment after the focused composer receives typed input", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.comment.list", result: commentListResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.comment.add", result: commentAddResponse },
    ]);

    render(<App services={services} />);

    const composerFrame = await screen.findByTestId("task-comment-input-frame");
    const composer = within(composerFrame).getByRole("textbox");
    composer.focus();
    expect(composer).toHaveFocus();
    fireEvent.change(composer, {
      target: { value: "Focused composer comment" },
    });
    fireEvent.click(screen.getByTestId("task-comment-save"));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.comment.add",
        params: { author: guiTaskCommentAuthor, body: "Focused composer comment", task_id: "task-1" },
      });
    });
  });

  it("renders task description markdown with block typography", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        result: {
          task: {
            ...taskDetailNoInboxResponse.task,
            body: "# Release plan\n\n- Restore bullets\n- Restore headings\n\nUse `kent`.",
          },
        },
      },
      { method: "workflow.task.comment.list", result: commentListResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    render(<App services={services} />);

    const description = await screen.findByRole("textbox", { name: "Description" });
    expect(within(description).getByTestId("markdown-text")).toHaveClass("markdown-text");
    expect(within(description).getByRole("heading", { level: 1, name: "Release plan" })).toBeInTheDocument();
    expect(within(description).getAllByRole("listitem")).toHaveLength(2);
    expect(within(description).getByText("kent")).toBeInTheDocument();
  });

  it("surfaces failed comment deletes through the status toast surface", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.comment.list", result: commentListResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.comment.delete", error: new Error("delete failed") },
    ]);

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: "Delete comment" }));

    await waitFor(() => {
      expect(toastCount()).toBe(1);
    });
  });

  it("renders task comments from paginated comment pages and loads the next page", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const detailWithoutInlineComments = {
      task: {
        ...taskDetailNoInboxResponse.task,
        comments: [],
      },
    };
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: detailWithoutInlineComments },
      {
        method: "workflow.task.comment.list",
        handler: (params: JsonValue) => {
          if (isJsonObject(params) && params.page_token === "cursor-2") {
            return secondCommentListResponse;
          }
          return firstCommentListResponse;
        },
      },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    render(<App services={services} />);

    expect(await screen.findByText("First paged comment")).toBeInTheDocument();
    expect(await screen.findByText("Second paged comment")).toBeInTheDocument();
    expect(services.transport.calls).toContainEqual({
      method: "workflow.task.comment.list",
      params: { task_id: "task-1", page_size: 40, page_token: "" },
    });
    expect(services.transport.calls).toContainEqual({
      method: "workflow.task.comment.list",
      params: { task_id: "task-1", page_size: 40, page_token: "cursor-2" },
    });
    expect(screen.queryByRole("region", { name: "Comments" })).not.toBeInTheDocument();
  });

  it("requires commentary when answering a task question with Neither", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "ask.listPendingBySession", result: pendingAskResponse },
      { method: "workflow.task.question.answer", result: {} },
    ]);

    render(<App services={services} />);

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Use option A/u })).toBeChecked();
    fireEvent.click(within(question).getByRole("radio", { name: "Neither" }));
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeDisabled();

    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Use a different path." },
    });
    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.ask_id).toBe("ask-1");
      expect(params.freeform_answer).toBe("Use a different path.");
      expect(params.selected_option_number).toBeUndefined();
    });
  });

  it("preserves commentary when switching between task question options", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "ask.listPendingBySession", result: pendingAskResponse },
      { method: "workflow.task.question.answer", result: {} },
    ]);

    render(<App services={services} />);

    const question = await screen.findByRole("region", { name: "Question" });
    const recommendedOption = await within(question).findByRole("radio", { name: /Use option A/u });
    const commentary = within(question).getByRole("textbox", { name: "Commentary" });
    fireEvent.change(commentary, { target: { value: "Keep the rationale." } });
    fireEvent.click(within(question).getByRole("radio", { name: "Neither" }));
    fireEvent.click(recommendedOption);
    expect(commentary).toHaveValue("Keep the rationale.");

    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.freeform_answer).toBe("Keep the rationale.");
      expect(params.selected_option_number).toBe(1);
    });
  });

  it("renders task question options from attention when pending asks are not available", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const detailWithAttentionOptions = {
      task: {
        ...taskDetailResponse.task,
        attention: taskDetailResponse.task.attention.map((item) =>
          item.kind === "question"
            ? {
                ...item,
                message: "Choose snack",
                recommended_option_index: 2,
                suggestions: ["Trail mix", "Dark chocolate", "Pistachios"],
              }
            : item,
        ),
      },
    };
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: detailWithAttentionOptions },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "ask.listPendingBySession", result: { Asks: [] } },
    ]);

    render(<App services={services} />);

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Trail mix/u })).toBeInTheDocument();
    expect(within(question).getByRole("radio", { name: /Dark chocolate/u })).toBeChecked();
    expect(within(question).getByRole("radio", { name: /Pistachios/u })).toBeInTheDocument();
  });

  it("focuses only the first unresolved matching question from a batched notification target", async () => {
    window.history.pushState(null, "", "/");
    const scrollTargets: HTMLElement[] = [];
    const restoreScrollIntoView = installScrollIntoViewSpy(scrollTargets);
    const native = nativeBridgeWithActivation();
    const detailWithQuestionBatch = {
      task: {
        ...taskDetailResponse.task,
        attention: [
          {
            ...questionAttention,
            id: "attention-ask-1",
            ask_id: "ask-1",
          },
          {
            ...questionAttention,
            id: "attention-ask-2",
            ask_id: "ask-2",
          },
        ],
      },
    };
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: detailWithQuestionBatch },
        { method: "workflow.task.activity.list", result: activityResponse },
        { method: "ask.listPendingBySession", result: { Asks: [] } },
        { method: "workflow.task.question.answer", result: {} },
      ],
      native.bridge,
    );

    try {
      render(<App services={services} />);
      await waitFor(() => {
        expect(native.hasActivationHandler()).toBe(true);
      });

      act(() => {
        native.triggerActivation({
          id: "question_batch:run-1:batch-1",
          target: {
            kind: "task_detail",
            taskID: "task-1",
            focus: { kind: "question", askIDs: ["ask-1", "ask-2"] },
          },
        });
      });

      await waitFor(() => {
        expect(scrollTargets).toHaveLength(1);
      });
      const focusedTarget = scrollTargets[0];
      if (focusedTarget === undefined) {
        throw new Error("Expected one task detail attention row to receive initial focus.");
      }
      const focusedAnswer = within(focusedTarget).getByRole("textbox");
      const focusedSubmit = within(focusedTarget).getByRole("button");
      fireEvent.change(focusedAnswer, { target: { value: "operator answer" } });
      fireEvent.click(focusedSubmit);
      await waitFor(() => {
        const params = callParams(services.transport.calls, "workflow.task.question.answer");
        expect(params.ask_id).toBe("ask-1");
      });
    } finally {
      restoreScrollIntoView();
    }
  });

  it("renders approval snapshots as route, commentary, and copyable output values", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const copied: string[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.task.get", result: taskDetailResponse },
        { method: "workflow.task.activity.list", result: activityResponse },
        { method: "ask.listPendingBySession", result: pendingAskResponse },
      ],
      nativeBridgeWithClipboard(copied),
    );

    render(<App services={services} />);

    const approval = await screen.findByRole("region", { name: "Approval" });
    expect(within(approval).queryByRole("heading", { name: "Approval" })).not.toBeInTheDocument();
    expect(within(approval).queryByText("Approval snapshot")).not.toBeInTheDocument();
    expect(within(approval).queryByText("Version")).not.toBeInTheDocument();
    expect(within(approval).queryByText("Approve transition")).not.toBeInTheDocument();
    const routeActionRow = within(approval).getByTestId("task-approval-route-action-row");
    expect(within(routeActionRow).getByTestId("workflow-edge-route-source")).toHaveTextContent("Implement");
    expect(within(routeActionRow).getByTestId("workflow-edge-route-target")).toHaveTextContent("Ship");
    expect(within(routeActionRow).getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(within(routeActionRow).queryByText("Looks good")).not.toBeInTheDocument();
    expect(within(routeActionRow).queryByRole("button", { name: "ok" })).not.toBeInTheDocument();
    expect(within(approval).getByText("Looks good")).toBeInTheDocument();

    fireEvent.click(within(approval).getByRole("button", { name: "ok" }));

    await waitFor(() => {
      expect(copied).toEqual(["ok"]);
    });
  });

  it("confirms task cancellation in a popover without inline helper copy", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "ask.listPendingBySession", result: pendingAskResponse },
      { method: "workflow.task.cancel", result: {} },
    ]);

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: "Cancel task" }));

    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.cancel",
        params: { task_id: "task-1" },
      });
    });
  });

  it("edits active task title and description", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    let currentTitle = taskDetailNoInboxResponse.task.summary.title;
    let currentBody = taskDetailNoInboxResponse.task.body;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        handler: () => ({
          task: {
            ...taskDetailNoInboxResponse.task,
            summary: { ...taskDetailNoInboxResponse.task.summary, title: currentTitle },
            body: currentBody,
          },
        }),
      },
      { method: "workflow.task.activity.list", result: activityResponse },
      {
        method: "workflow.task.update",
        handler: (params: JsonValue) => {
          if (isJsonObject(params)) {
            currentTitle = typeof params.title === "string" ? params.title : currentTitle;
            currentBody = typeof params.body === "string" ? params.body : currentBody;
          }
          return taskUpdateResponse;
        },
      },
    ]);

    render(<App services={services} />);

    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue("Resolve blocker");
    expect(screen.queryByRole("region", { name: "Inbox" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Renamed task" } });
    expect(screen.queryByRole("button", { name: "Save title" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.update",
        params: {
          task_id: "task-1",
          title: "Renamed task",
          body: "Need operator input",
        },
      });
    });

    fireEvent.focus(screen.getByRole("textbox", { name: "Description" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Updated details" },
    });
    expect(screen.queryByTestId("task-description-save")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.update",
        params: {
          task_id: "task-1",
          title: "Renamed task",
          body: "Updated details",
        },
      });
    });
  });

  it("saves description-only task edits through the shared save action", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: taskDetailNoInboxResponse },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.update", result: taskUpdateResponse },
    ]);

    render(<App services={services} />);

    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue("Resolve blocker");
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();

    fireEvent.focus(screen.getByRole("textbox", { name: "Description" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Updated description only" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => {
      expect(services.transport.calls).toContainEqual({
        method: "workflow.task.update",
        params: {
          task_id: "task-1",
          title: "Resolve blocker",
          body: "Updated description only",
        },
      });
    });
  });

  it("refreshes the standalone task surface when a server event mutates the task", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    let hasQuestion = false;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        handler: () => ({
          task: {
            ...taskDetailNoInboxResponse.task,
            summary: {
              ...taskDetailNoInboxResponse.task.summary,
              updated_at_unix_ms: hasQuestion ? 99 : 2,
            },
            attention: hasQuestion ? [questionAttention] : [],
          },
        }),
      },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "ask.listPendingBySession", result: { Asks: [] } },
    ]);

    render(<App services={services} />);

    await screen.findByRole("textbox", { name: "Title" });
    expect(screen.queryByRole("region", { name: "Question" })).not.toBeInTheDocument();

    hasQuestion = true;
    act(() => {
      services.transport.emit("workflow.event", taskQuestionWaitingEvent);
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Trail mix/u })).toBeInTheDocument();
  });

  it("keeps unsaved title and description edits across a live refresh", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    let serverBody = taskDetailNoInboxResponse.task.body;
    let serverUpdatedAt = 2;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        handler: () => ({
          task: {
            ...taskDetailNoInboxResponse.task,
            summary: { ...taskDetailNoInboxResponse.task.summary, updated_at_unix_ms: serverUpdatedAt },
            body: serverBody,
          },
        }),
      },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    render(<App services={services} />);

    const description = await screen.findByRole("textbox", { name: "Description" });
    fireEvent.focus(description);
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), { target: { value: "Half-written notes" } });
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();

    // The task advances on the server and an event fires; the in-progress edit
    // must survive the background refresh.
    serverBody = "Agent rewrote the body";
    serverUpdatedAt = 99;
    act(() => {
      services.transport.emit("workflow.event", taskUpdatedEvent);
    });

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.get")).toBeGreaterThan(1);
    });
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Half-written notes");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
  });

  it("follows server title updates on a clean surface without unsaved edits", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    let serverTitle = "Resolve blocker";
    let serverUpdatedAt = 2;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        handler: () => ({
          task: {
            ...taskDetailNoInboxResponse.task,
            summary: {
              ...taskDetailNoInboxResponse.task.summary,
              title: serverTitle,
              updated_at_unix_ms: serverUpdatedAt,
            },
          },
        }),
      },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    render(<App services={services} />);

    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue("Resolve blocker");

    serverTitle = "Renamed by agent";
    serverUpdatedAt = 99;
    act(() => {
      services.transport.emit("workflow.event", taskUpdatedEvent);
    });

    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Renamed by agent");
    });
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });

});

function nativeBridgeWithClipboard(copied: string[]): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      clipboard: { ...base.capabilities.clipboard, writeText: true },
    },
    clipboard: {
      ...base.clipboard,
      async writeText(value): Promise<void> {
        copied.push(value);
      },
    },
  };
}

function nativeBridgeWithActivation(): Readonly<{
  bridge: NativeBridge;
  hasActivationHandler(): boolean;
  triggerActivation(activation: NativeNotificationActivation): void;
}> {
  const base = createBrowserNativeBridge();
  let activationHandler: ((activation: NativeNotificationActivation) => void) | null = null;
  return {
    bridge: {
      ...base,
      notifications: {
        ...base.notifications,
        async onActivated(handler: (activation: NativeNotificationActivation) => void): Promise<() => void> {
          activationHandler = handler;
          return () => {
            activationHandler = null;
          };
        },
      },
    },
    hasActivationHandler(): boolean {
      return activationHandler !== null;
    },
    triggerActivation(activation: NativeNotificationActivation): void {
      activationHandler?.(activation);
    },
  };
}

function installScrollIntoViewSpy(targets: HTMLElement[]): () => void {
  const originalDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value(this: HTMLElement) {
      targets.push(this);
    },
  });
  return () => {
    if (originalDescriptor === undefined) {
      Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
      return;
    }
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", originalDescriptor);
  };
}

function nativeBridgeWithFiles({
  available,
  checked,
  opened,
}: Readonly<{
  available: boolean;
  checked: NativeFileTarget[];
  opened: NativeFileTarget[];
}>): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      files: { ...base.capabilities.files, open: true, stat: true },
    },
    files: {
      ...base.files,
      async fileAvailable(target) {
        checked.push(target);
        return available;
      },
      async openFile(target) {
        opened.push(target);
      },
    },
  };
}

const workflow = {
  workflow_id: "workflow-1",
  display_name: "Delivery",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const workspace = {
  workspace_id: "workspace-1",
  display_name: "Main",
  root_path: "/tmp/project",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const taskActions = {
  can_start: false,
  can_interrupt: true,
  can_resume: false,
  can_cancel: true,
};

const attentionBase = {
  project_id: "project-1",
  workflow_id: "workflow-1",
  task_id: "task-1",
  task_short_id: "T-1",
  task_title: "Resolve blocker",
  occurred_at_unix_ms: 1,
};

const taskDetailResponse = {
  task: {
    summary: {
      id: "task-1",
      project_id: "project-1",
      workflow_id: "workflow-1",
      short_id: "T-1",
      title: "Resolve blocker",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 2,
      done: false,
      canceled_at_unix_ms: 0,
    },
    project: { display_name: "Project" },
    workflow,
    body: "Need operator input",
    source_workspace: workspace,
    managed_worktree: { canonical_root: "/tmp/worktree" },
    status: {
      kind: "running",
      label: "Running",
      native_state: "running",
      node_ids: ["node-1"],
      run_ids: ["run-1"],
      attention_types: ["question", "approval"],
    },
    actions: taskActions,
    attention: [
      {
        ...attentionBase,
        id: "attention-question",
        kind: "question",
        run_id: "run-1",
        session_id: "session-1",
        ask_id: "ask-1",
        task_transition_id: "",
        message: "",
      },
      {
        ...attentionBase,
        id: "attention-approval",
        kind: "approval",
        run_id: "run-1",
        session_id: "session-1",
        ask_id: "",
        task_transition_id: "transition-1",
        message: "Approve transition",
      },
    ],
    runs: [
      {
        id: "run-1",
        task_id: "task-1",
        placement_id: "placement-1",
        node_id: "node-1",
        session_id: "session-1",
        session_name: "Kent session",
        role: "agent",
        status: "running",
        generation: 1,
        waiting_ask_id: "ask-1",
        started_at_unix_ms: 1,
        completed_at_unix_ms: 0,
        interrupted_at_unix_ms: 0,
      },
    ],
    transitions: [
      {
        id: "transition-1",
        transition_id: "ship",
        transition_display_name: "Ship",
        source_node_display_name: "Implement",
        state: "pending_approval",
        commentary: "Looks good",
        output_values: { result: "ok" },
        edges: [
          {
            id: "transition-edge-1",
            edge_key: "ship",
            target_node_display_name: "Ship",
            state: "pending",
            requires_approval: true,
            output_requirements: [],
          },
        ],
        workflow_revision_seen: 7,
        created_at_unix_ms: 2,
        applied_at_unix_ms: 0,
      },
    ],
    comments: [
      {
        id: "comment-1",
        task_id: "task-1",
        body: "Existing comment",
        author: guiTaskCommentAuthor,
        created_at_unix_ms: 1,
        updated_at_unix_ms: 1,
      },
    ],
  },
};

const taskDetailResponseWithNewerActiveRun = {
  task: {
    ...taskDetailResponse.task,
    runs: [
      ...taskDetailResponse.task.runs,
      {
        ...taskDetailResponse.task.runs[0],
        id: "run-2",
        session_id: "session-2",
        started_at_unix_ms: 2,
      },
    ],
  },
};

const taskDetailNoInboxResponse = {
  task: {
    ...taskDetailResponse.task,
    attention: [],
    transitions: [],
  },
};

const taskDetailResponseWithScriptRun = {
  task: {
    ...taskDetailNoInboxResponse.task,
    actions: { ...taskActions, can_interrupt: false },
    runs: [
      {
        id: "run-script",
        task_id: "task-1",
        placement_id: "placement-script",
        node_id: "node-script",
        node_kind: "script",
        script_path: "scripts/run",
        session_id: "",
        session_name: "",
        role: "script",
        status: "interrupted",
        generation: 1,
        waiting_ask_id: "",
        started_at_unix_ms: 1,
        completed_at_unix_ms: 0,
        interrupted_at_unix_ms: 2,
        interruption_reason: "script failed",
        interruption_detail_json: '{"kind":"script_failure"}',
      },
    ],
  },
};

const taskDetailResponseWithInterruptedScriptRun = {
  task: {
    ...taskDetailResponseWithScriptRun.task,
    actions: { ...taskActions, can_interrupt: false, can_resume: true },
    attention: [
      {
        ...attentionBase,
        id: "attention-interrupted",
        kind: "interrupted_run",
        run_id: "run-script",
        session_id: "",
        ask_id: "",
        task_transition_id: "",
        message: "Script failed",
        detail_json: '{"kind":"script_failure","stderr":"permission denied"}',
      },
    ],
  },
};

const questionAttention = {
  ...attentionBase,
  id: "attention-question",
  kind: "question",
  run_id: "run-1",
  session_id: "session-1",
  ask_id: "ask-1",
  task_transition_id: "",
  message: "Choose snack",
  recommended_option_index: 1,
  suggestions: ["Trail mix", "Dark chocolate"],
};

const taskQuestionWaitingEvent = {
  event: {
    resource: "task",
    action: "question_waiting",
    changed_ids: ["task-1", "run-1", "ask-1"],
    project_id: "project-1",
    workflow_id: "workflow-1",
  },
};

const taskUpdatedEvent = {
  event: {
    resource: "task",
    action: "updated",
    changed_ids: ["task-1"],
    project_id: "project-1",
    workflow_id: "workflow-1",
  },
};

const activityResponse = {
  items: [
    {
      activity_id: "activity-1",
      type: "comment",
      task_id: "task-1",
      occurred_at_unix_ms: 2,
      updated_at_unix_ms: 2,
      actor: "GUI",
      summary: "Comment added",
      comment: null,
      transition: null,
      run: null,
      attention: null,
    },
  ],
  next_page_token: "",
  generated_at_unix_ms: 3,
};

const pendingAskResponse = {
  Asks: [
    {
      AskID: "ask-1",
      SessionID: "session-1",
      Question: "Choose path",
      Suggestions: ["Use option A", "Use option B"],
      RecommendedOptionIndex: 1,
      CreatedAt: "2026-05-16T00:00:00Z",
    },
  ],
};

const commentAddResponse = {
  comment: {
    id: "comment-2",
    task_id: "task-1",
    body: "Fresh comment",
    author: guiTaskCommentAuthor,
    created_at_unix_ms: 4,
    updated_at_unix_ms: 4,
  },
};

const commentListResponse = {
  comments: taskDetailResponse.task.comments,
  next_page_token: "",
};

const firstCommentListResponse = {
  comments: [
    {
      id: "comment-page-1",
      task_id: "task-1",
      body: "First paged comment",
      author: guiTaskCommentAuthor,
      created_at_unix_ms: 5,
      updated_at_unix_ms: 5,
    },
  ],
  next_page_token: "cursor-2",
};

const secondCommentListResponse = {
  comments: [
    {
      id: "comment-page-2",
      task_id: "task-1",
      body: "Second paged comment",
      author: guiTaskCommentAuthor,
      created_at_unix_ms: 6,
      updated_at_unix_ms: 6,
    },
  ],
  next_page_token: "",
};

const taskUpdateResponse = {
  task: {
    id: "task-1",
  },
};

function callParams(
  calls: readonly Readonly<{ method: string; params: JsonValue }>[],
  method: string,
): JsonObject {
  const params = calls.find((call) => call.method === method)?.params;
  if (!isJsonObject(params)) {
    throw new Error(`Missing object params for ${method}.`);
  }
  return params;
}

function getCallCount(
  calls: readonly Readonly<{ method: string; params: JsonValue }>[],
  method: string,
): number {
  return calls.filter((call) => call.method === method).length;
}

function isJsonObject(value: JsonValue | undefined): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function toastCount(): number {
  return statusToastHarness.notices.size;
}

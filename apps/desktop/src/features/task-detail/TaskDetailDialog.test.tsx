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
import type { JsonValue } from "../../api/json";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";
import {
  activityResponse,
  callParams,
  commentAddResponse,
  commentListResponse,
  firstCommentListResponse,
  isJsonObject,
  pendingAskResponse,
  questionAttention,
  secondCommentListResponse,
  taskDetailNoInboxResponse,
  taskDetailResponse,
  taskDetailResponseWithInterruptedScriptRun,
  taskDetailResponseWithNewerActiveRun,
  taskDetailResponseWithScriptRun,
} from "../../testSupport/taskDetailFixtures";
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
        {
          method: "workflow.task.approve",
          result: {
            outcome: "approved",
            approved: {
              transition_id: "transition-1",
              task_id: "task-1",
              state: "approved",
            },
          },
        },
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
      const params = callParams(services.transport.calls, "workflow.task.approve");
      expect(params.task_transition_id).toBe("transition-1");
      expect(
        services.transport.calls.find((call) => call.method === "workflow.task.approve")?.options,
      ).toEqual({
        timeoutMs: null,
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

  it("renders and submits runtime approval prompts through the task question path", async () => {
    window.history.pushState(null, "", "/tasks/task-1");
    const detailWithRuntimeApprovalQuestion = {
      task: {
        ...taskDetailResponse.task,
        attention: taskDetailResponse.task.attention.map((item) =>
          item.kind === "question"
            ? {
                ...item,
                message: "Approve protected path?",
                question: {
                  kind: "approval",
                  approval_decisions: ["allow_once", "allow_session", "deny"],
                },
                recommended_option_index: 0,
                suggestions: [],
              }
            : item,
        ),
      },
    };
    const services = createTestServices([
      ...startupRoutes,
      { method: "workflow.task.get", result: detailWithRuntimeApprovalQuestion },
      { method: "workflow.task.activity.list", result: activityResponse },
      { method: "workflow.task.question.answer", result: {} },
    ]);

    render(<App services={services} />);

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByText("Approve protected path?")).toBeInTheDocument();
    expect(within(question).getByRole("radio", { name: "Allow once" })).toBeChecked();
    expect(within(question).getByRole("radio", { name: "Allow for this session" })).toBeInTheDocument();
    expect(within(question).getByRole("radio", { name: "Deny" })).toBeInTheDocument();
    expect(within(question).queryByRole("radio", { name: "Neither" })).not.toBeInTheDocument();
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeEnabled();

    fireEvent.click(within(question).getByRole("radio", { name: "Deny" }));
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeDisabled();
    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Not safe." },
    });
    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.ask_id).toBe("ask-1");
      expect(params.approval).toEqual({ decision: "deny", commentary: "Not safe." });
      expect(params.selected_option_number).toBeUndefined();
      expect(params.freeform_answer).toBeUndefined();
    });
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

function toastCount(): number {
  return statusToastHarness.notices.size;
}

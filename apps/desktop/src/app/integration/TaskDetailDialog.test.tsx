import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeFileTarget,
  type NativeNotificationActivation,
} from "@/test-support/native-bridge";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { vi } from "vitest";

import { App } from "../startup/App";
import { errorMessage, guiTaskCommentAuthor } from "@/api";
import type { JsonValue } from "@/api";
import { appI18n } from "@/i18n";
import type { FakeRoute } from "@/test-support/api";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import {
  activityResponse,
  callParams,
  commentAddResponse,
  commentListResponse,
  emptyTaskAttentionResponse,
  firstCommentListResponse,
  getCallCount,
  interruptedTaskAttentionResponse,
  isJsonObject,
  pendingAskResponse,
  questionAttention,
  secondCommentListResponse,
  taskDetailNoInboxResponse,
  taskDetailResponse,
  taskAttentionResponse,
  taskDetailResponseWithInterruptedScriptRun,
  taskDetailResponseWithNewerActiveRun,
  taskDetailResponseWithScriptRun,
} from "@/test-support/task-detail";
import { showStatusToast, type StatusNotice } from "@/ui";
import type * as uiModule from "@/ui";

const statusToastHarness = vi.hoisted(() => ({
  notices: new Map<string, StatusNotice>(),
}));

vi.mock("@/ui", async (importOriginal) => {
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

type TaskDetailFixtureOptions = Readonly<{
  attention?: JsonValue;
  asks?: unknown;
  comments?: unknown;
  nativeBridge?: NativeBridge | undefined;
  path?: string | undefined;
  routes?: readonly FakeRoute[] | undefined;
}>;

function taskDetailFixture(
  task: JsonValue,
  {
    asks,
    attention = taskAttentionFixture(task),
    comments,
    nativeBridge,
    path = "/tasks/task-1",
    routes = [],
  }: TaskDetailFixtureOptions = {},
): ReturnType<typeof createTestServices> {
  window.history.pushState(null, "", path);
  const services = createTestServices(
    [
      ...startupRoutes,
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [],
          },
        },
      },
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        },
      },
      { method: "workflow.task.get", result: task },
      { method: "workflow.task.attention.list", result: attention },
      ...(comments === undefined ? [] : [{ method: "workflow.task.comment.list", result: comments }]),
      { method: "workflow.task.activity.list", result: activityResponse },
      ...(asks === undefined ? [] : [{ method: "ask.listPendingBySession", result: asks }]),
      ...routes,
    ],
    nativeBridge,
  );
  render(<App services={services} />);
  return services;
}

function taskAttentionFixture(task: JsonValue): JsonValue {
  if (task === taskDetailResponseWithInterruptedScriptRun) {
    return interruptedTaskAttentionResponse;
  }
  if (isJsonObject(task) && isJsonObject(task.task) && task.task.attention_count === 0) {
    return emptyTaskAttentionResponse;
  }
  return taskAttentionResponse;
}

describe("TaskDetailSurface", () => {
  beforeEach(() => {
    statusToastHarness.notices.clear();
    vi.mocked(showStatusToast).mockClear();
  });

  it("renders direct task route inline with inbox, comments, approvals, questions, and CLI actions", async () => {
    const copied: string[] = [];
    const services = taskDetailFixture(taskDetailResponseWithNewerActiveRun, {
      asks: pendingAskResponse,
      comments: commentListResponse,
      nativeBridge: nativeBridgeWithClipboard(copied),
      routes: [
        { method: "workflow.task.question.answer", result: {} },
        {
          method: "workflow.task.approve",
          result: {
            outcome: "applied",
            applied: {
              transition_id: "transition-1",
              task_id: "task-1",
              state: "approved",
            },
          },
        },
        { method: "workflow.task.comment.add", result: commentAddResponse },
        { method: "workflow.task.comment.replace", result: {} },
      ],
    });

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

  it("refreshes core detail and task attention after approval without collapsing the rendered task", async () => {
    const services = taskDetailFixture(taskDetailResponse, {
      routes: [
        {
          method: "workflow.task.get",
          handler: (_params, callIndex) =>
            callIndex === 0
              ? taskDetailResponse
              : {
                  task: {
                    ...taskDetailResponse.task,
                    attention_count: 0,
                    transitions: [],
                  },
                },
        },
        {
          method: "workflow.task.attention.list",
          handler: (_params, callIndex) =>
            callIndex === 0 ? taskAttentionResponse : emptyTaskAttentionResponse,
        },
        {
          method: "workflow.task.approve",
          result: {
            outcome: "applied",
            applied: {
              transition_id: "transition-1",
              task_id: "task-1",
              state: "approved",
            },
          },
        },
      ],
    });

    const approval = await screen.findByRole("region", { name: "Approval" });
    fireEvent.click(within(approval).getByRole("button", { name: "Approve" }));

    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Approval" })).not.toBeInTheDocument();
      expect(getCallCount(services.transport.calls, "workflow.task.get")).toBeGreaterThanOrEqual(2);
      expect(getCallCount(services.transport.calls, "workflow.task.attention.list")).toBeGreaterThanOrEqual(
        2,
      );
    });
    expect(screen.getByDisplayValue("Resolve blocker")).toBeInTheDocument();
    expect(screen.getByTestId("task-detail-island-stack")).toBeInTheDocument();
  });

  it("continues the exact pending approval with a task-local execution target", async () => {
    const services = taskDetailFixture(taskDetailResponseWithNewerActiveRun, {
      asks: pendingAskResponse,
      comments: commentListResponse,
      routes: [
        {
          method: "workflow.task.approve",
          handler: (_params, callIndex) =>
            callIndex === 0
              ? {
                  outcome: "selection_required",
                  selection_required: { reason: "policy_requires_selection" },
                }
              : {
                  outcome: "applied",
                  applied: {
                    transition_id: "transition-1",
                    task_id: "task-1",
                    state: "approved",
                  },
                },
        },
      ],
    });

    fireEvent.click(await screen.findByRole("button", { name: appI18n.t("task.approve") }));
    const dialog = await screen.findByRole("dialog", {
      name: appI18n.t("executionTargetContinuation.title"),
    });
    expect(within(dialog).getAllByRole("radio")).toHaveLength(4);
    expect(
      within(dialog).getByRole("radio", {
        name: new RegExp(appI18n.t("executionTargetContinuation.mode_default_branch"), "u"),
      }),
    ).toBeChecked();

    fireEvent.click(
      within(dialog).getByRole("button", {
        name: appI18n.t("executionTargetContinuation.continue"),
      }),
    );

    await waitFor(() => {
      expect(
        screen.queryByRole("dialog", {
          name: appI18n.t("executionTargetContinuation.title"),
        }),
      ).not.toBeInTheDocument();
    });
    const approvalCalls = services.transport.calls.filter((call) => call.method === "workflow.task.approve");
    expect(approvalCalls).toHaveLength(2);
    const initialParams = approvalCalls[0]?.params;
    const continuationParams = approvalCalls[1]?.params;
    if (!isJsonObject(initialParams) || !isJsonObject(continuationParams)) {
      throw new Error("approval calls require object params");
    }
    expect(initialParams.execution_target).toBeUndefined();
    expect(continuationParams.execution_target).toEqual({ mode: "default_branch" });
    expect(continuationParams.setup_operation_id).toBe(initialParams.setup_operation_id);
    expect(continuationParams.task_transition_id).toBe(initialParams.task_transition_id);
  });

  it("disables approval while the initial request is pending and submits it once", async () => {
    let resolveApproval:
      | ((response: {
          outcome: "applied";
          applied: {
            transition_id: string;
            task_id: string;
            state: string;
          };
        }) => void)
      | undefined;
    const approval = new Promise<{
      outcome: "applied";
      applied: {
        transition_id: string;
        task_id: string;
        state: string;
      };
    }>((resolve) => {
      resolveApproval = resolve;
    });
    const services = taskDetailFixture(taskDetailResponseWithNewerActiveRun, {
      asks: pendingAskResponse,
      comments: commentListResponse,
      routes: [{ method: "workflow.task.approve", handler: async () => approval }],
    });

    const approve = await screen.findByRole("button", { name: appI18n.t("task.approve") });
    fireEvent.click(approve);
    fireEvent.click(approve);

    expect(approve).toBeDisabled();
    expect(services.transport.calls.filter((call) => call.method === "workflow.task.approve")).toHaveLength(
      1,
    );

    await act(async () => {
      resolveApproval?.({
        outcome: "applied",
        applied: {
          transition_id: "transition-1",
          task_id: "task-1",
          state: "approved",
        },
      });
      await approval;
    });
  });

  it("renders queued task status from the typed server status", async () => {
    taskDetailFixture(
      {
        ...taskDetailNoInboxResponse,
        task: {
          ...taskDetailNoInboxResponse.task,
          status: {
            attention_types: [],
            kind: "queued",
            native_state: "queued",
            node_ids: ["node-1"],
            run_ids: ["run-1"],
          },
        },
      },
      { comments: commentListResponse },
    );

    expect(await screen.findByText("Queued")).toBeInTheDocument();
  });

  it("renders each managed execution path once in execution-target fact order", async () => {
    const copied: string[] = [];
    taskDetailFixture(
      {
        ...taskDetailNoInboxResponse,
        task: {
          ...taskDetailNoInboxResponse.task,
          execution_target: {
            ...taskDetailNoInboxResponse.task.execution_target,
            requested_ref: "requested-ref",
            current_branch: "managed-branch",
          },
        },
      },
      {
        comments: commentListResponse,
        nativeBridge: nativeBridgeWithClipboard(copied),
      },
    );

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });
    const definitions = propertyDefinitions(properties);
    const sourceWorkspace = definitionWithExactValue(definitions, "Main");
    const managedWorktree = definitionWithExactValue(definitions, "/tmp/worktree");

    expect(sourceWorkspace).toHaveTextContent("/tmp/project");
    expect(definitionsWithExactValue(definitions, "/tmp/project")).toHaveLength(1);
    expect(definitionsWithExactValue(definitions, "/tmp/worktree")).toHaveLength(1);
    expect(definitions.indexOf(managedWorktree)).toBeLessThan(
      definitions.indexOf(definitionWithExactValue(definitions, "requested-ref")),
    );
    expect(definitions.indexOf(managedWorktree)).toBeLessThan(
      definitions.indexOf(definitionWithExactValue(definitions, "0123456789ab")),
    );
    expect(definitions.indexOf(managedWorktree)).toBeLessThan(
      definitions.indexOf(definitionWithExactValue(definitions, "managed-branch")),
    );

    fireEvent.click(within(managedWorktree).getByRole("button"));

    await waitFor(() => {
      expect(copied).toEqual(["/tmp/worktree"]);
      expect(statusNotice("task-managed-worktree-path-copied")).toMatchObject({
        id: "task-managed-worktree-path-copied",
        tone: "success",
      });
    });

    const commitDefinition = definitionWithExactValue(definitions, "0123456789ab");
    fireEvent.click(within(commitDefinition).getByRole("button"));

    await waitFor(() => {
      expect(copied).toEqual(["/tmp/worktree", "0123456789abcdef0123456789abcdef01234567"]);
      expect(statusNotice("task-commit-copied")).toMatchObject({
        id: "task-commit-copied",
        tone: "success",
      });
    });
  });

  it("renders a no-managed source workspace path once", async () => {
    taskDetailFixture(
      {
        ...taskDetailNoInboxResponse,
        task: {
          ...taskDetailNoInboxResponse.task,
          execution_target: {
            mode: "none",
            effective_root: "/tmp/project",
            provenance: "resolved",
          },
        },
      },
      { comments: commentListResponse },
    );

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });

    expect(definitionsWithExactValue(propertyDefinitions(properties), "/tmp/project")).toHaveLength(1);
  });

  it("copies the source workspace path with its typed success notice", async () => {
    const copied: string[] = [];
    taskDetailFixture(taskDetailNoInboxResponse, {
      comments: commentListResponse,
      nativeBridge: nativeBridgeWithClipboard(copied),
    });

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });
    const sourceDefinition = definitionWithExactValue(propertyDefinitions(properties), "/tmp/project");

    fireEvent.click(within(sourceDefinition).getByRole("button"));

    await waitFor(() => {
      expect(copied).toEqual(["/tmp/project"]);
      expect(statusNotice("task-source-workspace-path-copied")).toMatchObject({
        id: "task-source-workspace-path-copied",
        tone: "success",
      });
    });
  });

  it("surfaces source workspace path clipboard rejection through its typed failure notice", async () => {
    const rejected = new Error("clipboard denied");
    taskDetailFixture(taskDetailNoInboxResponse, {
      comments: commentListResponse,
      nativeBridge: nativeBridgeRejectingClipboard(rejected),
    });

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });
    const sourceDefinition = definitionWithExactValue(propertyDefinitions(properties), "/tmp/project");

    fireEvent.click(within(sourceDefinition).getByRole("button"));

    await waitFor(() => {
      expect(statusNotice("task-source-workspace-path-copy-failed")).toMatchObject({
        body: errorMessage(rejected),
        id: "task-source-workspace-path-copy-failed",
        tone: "danger",
      });
    });
  });

  it("omits unavailable managed execution facts while retaining target history", async () => {
    taskDetailFixture(
      {
        ...taskDetailNoInboxResponse,
        task: {
          ...taskDetailNoInboxResponse.task,
          execution_target: {
            mode: "head",
            requested_ref: "legacy-source",
            commit_oid: "0123456789abcdef0123456789abcdef01234567",
            provenance: "legacy_observed",
            managed_worktree: {
              worktree_id: "worktree-1",
              display_name: "unavailable-branch",
              canonical_root: "/tmp/worktree",
              availability: "missing",
            },
          },
        },
      },
      { comments: commentListResponse },
    );

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });
    const definitions = propertyDefinitions(properties);

    expect(definitionsWithExactValue(definitions, "/tmp/worktree")).toHaveLength(0);
    expect(definitionsWithExactValue(definitions, "unavailable-branch")).toHaveLength(0);
    expect(definitionsWithExactValue(definitions, "legacy-source")).toHaveLength(1);
    expect(definitionsWithExactValue(definitions, "0123456789ab")).toHaveLength(1);
  });

  it("opens script files through native file capabilities without exposing CLI sessions", async () => {
    const opened: NativeFileTarget[] = [];
    const checked: NativeFileTarget[] = [];
    taskDetailFixture(taskDetailResponseWithScriptRun, {
      comments: commentListResponse,
      nativeBridge: nativeBridgeWithFiles({ available: true, checked, opened }),
    });

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
    const checked: NativeFileTarget[] = [];
    taskDetailFixture(taskDetailResponseWithScriptRun, {
      comments: commentListResponse,
      nativeBridge: nativeBridgeWithFiles({ available: false, checked, opened: [] }),
    });

    expect(await screen.findByRole("textbox", { name: "Description" })).toBeInTheDocument();
    await waitFor(() => {
      expect(checked).toContainEqual({ basePath: "/tmp/worktree", path: "scripts/run" });
      expect(screen.queryByRole("button", { name: "Open script" })).not.toBeInTheDocument();
    });
  });

  it("copies interrupted run structured details without rendering them inline", async () => {
    const copied: string[] = [];
    taskDetailFixture(taskDetailResponseWithInterruptedScriptRun, {
      comments: commentListResponse,
      nativeBridge: nativeBridgeWithClipboard(copied),
    });

    const interrupted = await screen.findByRole("region", { name: "Interrupted" });
    expect(within(interrupted).getByText("Script failed")).toBeInTheDocument();
    expect(within(interrupted).queryByText(/permission denied/u)).not.toBeInTheDocument();

    fireEvent.click(within(interrupted).getByRole("button", { name: "Copy interruption details" }));

    await waitFor(() => {
      expect(copied).toEqual(['{"kind":"script_failure","stderr":"permission denied"}']);
    });
  });

  it("surfaces failed comment saves through the status toast surface", async () => {
    taskDetailFixture(taskDetailResponse, {
      comments: commentListResponse,
      routes: [{ method: "workflow.task.comment.add", error: new Error("constraint failed") }],
    });

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
    const services = taskDetailFixture(taskDetailResponse, {
      comments: commentListResponse,
      routes: [{ method: "workflow.task.comment.add", result: commentAddResponse }],
    });

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
    taskDetailFixture(
      {
        task: {
          ...taskDetailNoInboxResponse.task,
          body: "# Release plan\n\n- Restore bullets\n- Restore headings\n\nUse `kent`.",
        },
      },
      { comments: commentListResponse },
    );

    const description = await screen.findByRole("textbox", { name: "Description" });
    expect(within(description).getByTestId("markdown-text")).toHaveClass("markdown-text");
    expect(within(description).getByRole("heading", { level: 1, name: "Release plan" })).toBeInTheDocument();
    expect(within(description).getAllByRole("listitem")).toHaveLength(2);
    expect(within(description).getByText("kent")).toBeInTheDocument();
  });

  it("surfaces failed comment deletes through the status toast surface", async () => {
    taskDetailFixture(taskDetailResponse, {
      comments: commentListResponse,
      routes: [{ method: "workflow.task.comment.delete", error: new Error("delete failed") }],
    });

    fireEvent.click(await screen.findByRole("button", { name: "Delete comment" }));

    await waitFor(() => {
      expect(toastCount()).toBe(1);
    });
  });

  it("renders task comments from paginated comment pages and loads the next page", async () => {
    const detailWithoutInlineComments = {
      task: {
        ...taskDetailNoInboxResponse.task,
        comments: [],
      },
    };
    const services = taskDetailFixture(detailWithoutInlineComments, {
      routes: [
        {
          method: "workflow.task.comment.list",
          handler: (params: JsonValue) => {
            if (isJsonObject(params) && params.page_token === "cursor-2") {
              return secondCommentListResponse;
            }
            return firstCommentListResponse;
          },
        },
      ],
    });

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
    const services = taskDetailFixture(taskDetailResponse, {
      asks: pendingAskResponse,
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Use option A/u })).toBeChecked();
    const neither = within(question).getByRole("radio", { name: "Neither" });
    expect(neither).not.toHaveValue("0");
    fireEvent.click(neither);
    expect(within(question).getByRole("button", { name: "Submit answer" })).toBeDisabled();

    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Use a different path." },
    });
    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.ask_id).toBe("ask-1");
      expect(params.freeform_answer).toBe("Use a different path.");
      expect(params.selected_option_number).toBeNull();
    });
  });

  it("preserves commentary when switching between task question options", async () => {
    const services = taskDetailFixture(taskDetailResponse, {
      asks: pendingAskResponse,
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });

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

  it("renders freeform-only ordinary questions without an option group and submits a null selection", async () => {
    const services = taskDetailFixture(taskDetailResponse, {
      asks: {
        Asks: [
          {
            ...pendingAskResponse.Asks[0],
            RecommendedOptionIndex: 0,
            Suggestions: [],
          },
        ],
      },
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(within(question).queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(within(question).queryByRole("radio")).not.toBeInTheDocument();
    expect(within(question).queryByRole("radio", { name: "Neither" })).not.toBeInTheDocument();

    fireEvent.change(within(question).getByRole("textbox", { name: "Commentary" }), {
      target: { value: "Use the manual route." },
    });
    fireEvent.click(within(question).getByRole("button", { name: "Submit answer" }));

    await waitFor(() => {
      const params = callParams(services.transport.calls, "workflow.task.question.answer");
      expect(params.freeform_answer).toBe("Use the manual route.");
      expect(params.selected_option_number).toBeNull();
    });
  });

  it("renders task question options from attention when pending asks are not available", async () => {
    const attentionWithOptions = {
      ...taskAttentionResponse,
      items: taskAttentionResponse.items.map((item) =>
        item.kind === "question"
          ? {
              ...item,
              message: "Choose snack",
              recommended_option_index: 2,
              suggestions: ["Trail mix", "Dark chocolate", "Pistachios"],
            }
          : item,
      ),
    };
    taskDetailFixture(taskDetailResponse, {
      attention: attentionWithOptions,
      asks: { Asks: [] },
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Trail mix/u })).toBeInTheDocument();
    expect(within(question).getByRole("radio", { name: /Dark chocolate/u })).toBeChecked();
    expect(within(question).getByRole("radio", { name: /Pistachios/u })).toBeInTheDocument();
  });

  it("renders and submits runtime approval prompts through the task question path", async () => {
    const attentionWithRuntimeApprovalQuestion = {
      ...taskAttentionResponse,
      items: taskAttentionResponse.items.map((item) =>
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
    };
    const services = taskDetailFixture(taskDetailResponse, {
      attention: attentionWithRuntimeApprovalQuestion,
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });

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
    const scrollTargets: HTMLElement[] = [];
    const restoreScrollIntoView = installScrollIntoViewSpy(scrollTargets);
    const native = nativeBridgeWithActivation();
    const attentionWithQuestionBatch = {
      items: [
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
      generated_at_unix_ms: 3,
    };
    const services = taskDetailFixture(taskDetailResponse, {
      attention: attentionWithQuestionBatch,
      asks: { Asks: [] },
      nativeBridge: native.bridge,
      path: "/",
      routes: [{ method: "workflow.task.question.answer", result: {} }],
    });

    try {
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

  it("copies approval output values through their typed notice policy", async () => {
    const copied: string[] = [];
    taskDetailFixture(taskDetailResponse, {
      asks: pendingAskResponse,
      nativeBridge: nativeBridgeWithClipboard(copied),
    });

    const approval = await screen.findByRole("region", { name: "Approval" });
    const routeActionRow = within(approval).getByTestId("task-approval-route-action-row");
    const action = within(routeActionRow).getByRole("button");
    const valueControl = within(approval)
      .getAllByRole("button")
      .find((control) => control !== action);
    expect(valueControl).toBeDefined();
    if (valueControl === undefined) {
      throw new Error("Expected one approval output value control outside the route action row.");
    }

    fireEvent.click(valueControl);

    await waitFor(() => {
      expect(copied).toEqual(["ok"]);
      expect(statusNotice("task-transition-output-copied-result")).toMatchObject({
        id: "task-transition-output-copied-result",
        tone: "success",
      });
    });
  });

  it("confirms task cancellation in a popover without inline helper copy", async () => {
    const services = taskDetailFixture(taskDetailResponse, {
      asks: pendingAskResponse,
      routes: [{ method: "workflow.task.cancel", result: {} }],
    });

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

function nativeBridgeRejectingClipboard(error: unknown): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      clipboard: { ...base.capabilities.clipboard, writeText: true },
    },
    clipboard: {
      ...base.clipboard,
      async writeText(): Promise<void> {
        throw error;
      },
    },
  };
}

function propertyDefinitions(region: HTMLElement): HTMLElement[] {
  return within(region).getAllByRole("definition");
}

function definitionsWithExactValue(definitions: readonly HTMLElement[], value: string): HTMLElement[] {
  return definitions.filter((definition) => within(definition).queryByText(value, { exact: true }) !== null);
}

function definitionWithExactValue(definitions: readonly HTMLElement[], value: string): HTMLElement {
  const matching = definitionsWithExactValue(definitions, value);
  expect(matching).toHaveLength(1);
  const definition = matching[0];
  if (definition === undefined) {
    throw new Error(`Expected one property definition with exact fixture value ${value}.`);
  }
  return definition;
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

function statusNotice(id: string): StatusNotice | undefined {
  return statusToastHarness.notices.get(id);
}

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactNode, useState } from "react";
import { afterEach, vi } from "vitest";

import type { TaskDetail } from "@/api";
import type { JsonValue } from "@/api";
import { createTestServices, startupRoutes, TestAppProviders } from "@/test-support/app-services";
import { appI18n } from "@/test-support/i18n";
import { installTestStorage } from "@/test-support/storage";
import * as uiModule from "@/ui";
import { ProjectLabelsProvider, TaskLabelAssignmentProvider, useProjectLabelCatalog } from "@/shared/labels";
import {
  activityResponse,
  createTaskDetailFixture,
  getCallCount,
  questionAttention,
  taskDetailNoInboxResponse,
  taskQuestionWaitingEvent,
  taskUpdateParamsSchema,
  taskUpdateResponse,
  taskUpdatedEvent,
} from "@/test-support/task-detail";
import { TaskDetailContent } from "./TaskDetailContent";
import { initialDescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { DescriptionIsland, type TaskDraft } from "./TaskDetailRows";
import { TaskDetailSurface } from "./TaskDetailSurface";
import { useTaskActivity, useTaskAttention, useTaskComments } from "./useTaskDetailData";

const priorityLabelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const betaLabelID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("TaskDetailSurface editing", () => {
  afterEach(() => {
    installTestStorage("localStorage");
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("surfaces a persisted label-filter read failure", async () => {
    const showStatusToast = vi.spyOn(uiModule, "showStatusToast");
    const storage = installTestStorage("localStorage");
    vi.spyOn(storage, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    mountTaskDetail(taskGetRoute());

    await waitFor(() => {
      expect(showStatusToast).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "task-label-load-error",
          tone: "danger",
        }),
      );
    });
  });

  it("edits active task title and description", async () => {
    let currentTitle = taskDetailNoInboxResponse.task.summary.title;
    let currentBody = taskDetailNoInboxResponse.task.body;
    const services = mountTaskDetail(
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
          const update = taskUpdateParamsSchema.safeParse(params);
          if (update.success) {
            currentTitle = update.data.title ?? currentTitle;
            currentBody = update.data.body ?? currentBody;
          }
          return taskUpdateResponse;
        },
      },
    );

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

  it("places the empty Labels property immediately after ID with an add affordance", async () => {
    mountTaskDetail(taskGetRoute());

    const properties = await screen.findByRole("region", {
      name: appI18n.t("task.properties"),
    });
    const terms = within(properties).getAllByRole("term");
    expect(terms.slice(0, 3).map((term) => term.getAttribute("aria-label"))).toEqual([
      appI18n.t("task.identifier", { defaultValue: "ID" }),
      appI18n.t("labels.filter"),
      appI18n.t("task.project"),
    ]);
    const trigger = within(properties).getByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    expect(await within(trigger).findByText(appI18n.t("labels.add"))).toBeVisible();
  });

  it("renders assigned catalog labels in the whole-value chooser trigger", async () => {
    mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [
              { id: betaLabelID, name: "Beta" },
              { id: priorityLabelID, name: "Priority" },
            ],
          },
        },
      },
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [betaLabelID, priorityLabelID],
          },
        },
      },
    );

    const trigger = await screen.findByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    expect(await within(trigger).findByText("Beta")).toBeVisible();
    expect(within(trigger).getByText("Priority")).toBeVisible();

    fireEvent.click(trigger);
    expect(await screen.findByRole("textbox", { name: appI18n.t("labels.search") })).toBeVisible();
  });

  it.each(["backlog", "active", "running", "interrupted", "done", "canceled"] as const)(
    "keeps label assignment available for %s tasks",
    async (kind) => {
      mountTaskDetail(
        taskGetRoute({
          status: {
            kind,
            native_state: kind,
            node_ids: [],
            run_ids: [],
            attention_types: [],
          },
        }),
      );

      const trigger = await screen.findByRole("button", {
        name: appI18n.t("labels.editAssignments"),
      });
      await within(trigger).findByText(appI18n.t("labels.add"));
      expect(trigger).toBeEnabled();
    },
  );

  it("rolls back only a failed optimistic label intent and exposes a successful Retry", async () => {
    const failedUpdate = deferred<unknown>();
    const services = mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [
              { id: betaLabelID, name: "Beta" },
              { id: priorityLabelID, name: "Priority" },
            ],
          },
        },
      },
      {
        method: "workflow.task.labels.get",
        result: {
          assignment: {
            task_id: "task-1",
            label_ids: [betaLabelID],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return failedUpdate.promise;
          }
          return {
            assignment: {
              task_id: "task-1",
              label_ids: [betaLabelID, priorityLabelID],
            },
          };
        },
      },
    );
    const trigger = await screen.findByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    expect(await within(trigger).findByText("Beta")).toBeVisible();
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByRole("button", { name: "Priority" }));

    expect(await within(trigger).findByText("Priority")).toBeVisible();
    failedUpdate.reject(new Error("assignment unavailable"));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(appI18n.t("labels.assignmentFailed"));
    expect(within(trigger).queryByText("Priority")).not.toBeInTheDocument();
    expect(within(trigger).getByText("Beta")).toBeVisible();

    fireEvent.click(within(alert).getByRole("button", { name: appI18n.t("app.retry") }));

    expect(await within(trigger).findByText("Priority")).toBeVisible();
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.update")).toBe(2);
    });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps an initial assignment read failure actionable", async () => {
    const services = mountTaskDetail(taskGetRoute(), {
      method: "workflow.task.labels.get",
      handler: (_params, callIndex) => {
        if (callIndex === 0) {
          throw new Error("assignment unavailable");
        }
        return {
          assignment: {
            task_id: "task-1",
            label_ids: [],
          },
        };
      },
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(appI18n.t("labels.assignmentRefreshFailed"));
    fireEvent.click(within(alert).getByRole("button", { name: appI18n.t("app.retry") }));

    expect(
      await screen.findByRole("button", { name: appI18n.t("labels.editAssignments") }),
    ).toHaveTextContent(appI18n.t("labels.add"));
    expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps a failed event reconciliation actionable", async () => {
    const services = mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityLabelID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.task.labels.get",
        handler: (_params, callIndex) => {
          if (callIndex === 1) {
            throw new Error("reconciliation unavailable");
          }
          return {
            assignment: {
              task_id: "task-1",
              label_ids: callIndex === 0 ? [] : [priorityLabelID],
            },
          };
        },
      },
    );
    await screen.findByRole("button", { name: appI18n.t("labels.editAssignments") });
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(1);
      expect(
        services.transport.subscriptions.filter(
          (subscription) => subscription.method === "workflow.subscribeProject",
        ),
      ).toHaveLength(2);
    });

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "labels_changed",
          occurred_at_unix_ms: 3,
          primary_entity_id: "task-1",
          project_id: "project-1",
          related_ids: [],
          resource: "task",
          workflow_id: "workflow-1",
        },
      });
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(appI18n.t("labels.assignmentRefreshFailed"));
    fireEvent.click(within(alert).getByRole("button", { name: appI18n.t("app.retry") }));

    const trigger = screen.getByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    expect(await within(trigger).findByText("Priority")).toBeVisible();
    expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(3);
    expect(getCallCount(services.transport.calls, "workflow.task.get")).toBe(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("removes a deleted pending label and ignores its late assignment response", async () => {
    const assignmentUpdate = deferred<unknown>();
    const services = mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => ({
          catalog: {
            project_id: "project-1",
            labels: callIndex === 0 ? [{ id: priorityLabelID, name: "Priority" }] : [],
          },
        }),
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => assignmentUpdate.promise,
      },
    );
    const trigger = await screen.findByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByRole("button", { name: "Priority" }));
    expect(await within(trigger).findByText("Priority")).toBeVisible();
    await waitFor(() => {
      expect(
        services.transport.subscriptions.filter(
          (subscription) => subscription.method === "workflow.subscribeProject",
        ),
      ).toHaveLength(2);
    });

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "deleted",
          occurred_at_unix_ms: 3,
          primary_entity_id: priorityLabelID,
          project_id: "project-1",
          related_ids: [],
          resource: "label",
          workflow_id: null,
        },
      });
    });
    await waitFor(() => {
      expect(within(trigger).queryByText("Priority")).not.toBeInTheDocument();
    });

    assignmentUpdate.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityLabelID],
      },
    });
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.update")).toBe(1);
      expect(getCallCount(services.transport.calls, "workflow.project.label.list")).toBe(2);
    });
    expect(within(trigger).queryByText("Priority")).not.toBeInTheDocument();
    expect(getCallCount(services.transport.calls, "workflow.task.get")).toBe(1);
  });

  it("closes the assignment lane before a late response after task deletion", async () => {
    const assignmentUpdate = deferred<unknown>();
    const services = mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityLabelID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => assignmentUpdate.promise,
      },
    );
    const trigger = await screen.findByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByRole("button", { name: "Priority" }));
    expect(await within(trigger).findByText("Priority")).toBeVisible();
    await waitFor(() => {
      expect(
        services.transport.subscriptions.filter(
          (subscription) => subscription.method === "workflow.subscribeProject",
        ),
      ).toHaveLength(2);
    });

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "deleted",
          occurred_at_unix_ms: 3,
          primary_entity_id: "task-1",
          project_id: "project-1",
          related_ids: [],
          resource: "task",
          workflow_id: "workflow-1",
        },
      });
    });
    await waitFor(() => {
      expect(within(trigger).queryByText("Priority")).not.toBeInTheDocument();
    });

    assignmentUpdate.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityLabelID],
      },
    });
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.update")).toBe(1);
    });
    expect(within(trigger).queryByText("Priority")).not.toBeInTheDocument();
    expect(trigger).toBeDisabled();
    expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(1);
  });

  it("keeps catalog loading and failure actionable inside the shared chooser", async () => {
    const services = mountTaskDetail(taskGetRoute(), {
      method: "workflow.project.label.list",
      handler: (_params, callIndex) => {
        if (callIndex === 0) {
          throw new Error("catalog unavailable");
        }
        return {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityLabelID, name: "Priority" }],
          },
        };
      },
    });
    const trigger = await screen.findByRole("button", {
      name: appI18n.t("labels.editAssignments"),
    });
    fireEvent.click(trigger);

    expect(await screen.findByText(appI18n.t("labels.loadFailed"))).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("app.retry") }));

    expect(await screen.findByRole("button", { name: "Priority" })).toBeVisible();
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.project.label.list")).toBe(2);
      expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(1);
    });
  });

  it("saves description-only task edits through the shared save action", async () => {
    const services = mountTaskDetail(taskGetRoute(), {
      method: "workflow.task.update",
      result: taskUpdateResponse,
    });

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

  it("expands an overflowing description for the mounted surface and keeps it expanded after editing", async () => {
    mountTaskDetail(taskGetRoute({ body: "Long description" }));

    const description = await screen.findByRole("textbox", { name: "Description" });
    makeDescriptionOverflow(description);

    const expand = await screen.findByRole("button", { name: "Expand" });
    vi.useFakeTimers();
    fireEvent.click(expand);
    expect(screen.getByTestId("task-description-expand")).toHaveAttribute("data-state", "exiting");
    expect(screen.getByTestId("task-description-fade")).toHaveAttribute("data-state", "exiting");
    act(() => {
      vi.advanceTimersByTime(140);
    });
    expect(screen.queryByRole("button", { name: "Expand" })).not.toBeInTheDocument();

    fireEvent.focus(description);
    expect(screen.getByRole("textbox", { name: "Description" })).toBeInstanceOf(HTMLTextAreaElement);
    fireEvent.blur(screen.getByRole("textbox", { name: "Description" }));
    expect(screen.queryByRole("button", { name: "Expand" })).not.toBeInTheDocument();
  });

  it("removes the description affordance immediately when reduced motion is requested", async () => {
    installReducedMotionMatchMedia();
    mountTaskDetail(taskGetRoute({ body: "Long description" }));

    const description = await screen.findByRole("textbox", { name: "Description" });
    makeDescriptionOverflow(description);

    fireEvent.click(await screen.findByRole("button", { name: "Expand" }));
    expect(screen.queryByTestId("task-description-expand")).not.toBeInTheDocument();
    expect(screen.queryByTestId("task-description-fade")).not.toBeInTheDocument();
  });

  it("resets description presentation when a mounted detail surface switches tasks", async () => {
    const taskOne = await createTaskDetailFixture();
    const taskTwo: TaskDetail = {
      ...taskOne,
      id: "task-2",
      shortID: "T-2",
      title: "Second task",
      body: "Second long description",
    };
    const { rerender } = renderWithAppProviders(<TaskDetailContentHarness detail={taskOne} />);

    fireEvent.focus(await screen.findByRole("textbox", { name: "Description" }));
    expect(screen.getByRole("textbox", { name: "Description" })).toBeInstanceOf(HTMLTextAreaElement);

    rerender(<TaskDetailContentHarness detail={taskTwo} />);

    const description = await screen.findByRole("textbox", { name: "Description" });
    expect(description).not.toBeInstanceOf(HTMLTextAreaElement);
    makeDescriptionOverflow(description);
    expect(await screen.findByRole("button", { name: "Expand" })).toBeInTheDocument();
  });

  it("retains expanded description presentation when the virtualized body row remounts", async () => {
    const { rerender } = renderWithAppProviders(<VirtualizedDescriptionHarness bodyVisible />);

    const description = screen.getByRole("textbox", { name: "Description" });
    makeDescriptionOverflow(description);
    fireEvent.click(await screen.findByRole("button", { name: "Expand" }));

    rerender(<VirtualizedDescriptionHarness bodyVisible={false} />);
    expect(screen.queryByTestId("task-description-input-frame")).not.toBeInTheDocument();

    rerender(<VirtualizedDescriptionHarness bodyVisible />);
    expect(screen.getByRole("textbox", { name: "Description" })).not.toBeInstanceOf(HTMLTextAreaElement);
    expect(screen.queryByRole("button", { name: "Expand" })).not.toBeInTheDocument();
  });

  it("refreshes the standalone task surface when a server event mutates the task", async () => {
    let hasQuestion = false;
    const services = mountTaskDetail(
      {
        method: "workflow.task.get",
        handler: () => ({
          task: {
            ...taskDetailNoInboxResponse.task,
            summary: {
              ...taskDetailNoInboxResponse.task.summary,
              updated_at_unix_ms: hasQuestion ? 99 : 2,
            },
            attention_count: hasQuestion ? 1 : 0,
          },
        }),
      },
      {
        method: "workflow.task.attention.list",
        handler: () => ({
          items: hasQuestion ? [questionAttention] : [],
          generated_at_unix_ms: hasQuestion ? 99 : 2,
        }),
      },
      { method: "ask.listPendingBySession", result: { Asks: [] } },
    );

    await screen.findByRole("textbox", { name: "Title" });
    expect(screen.queryByRole("region", { name: "Question" })).not.toBeInTheDocument();

    hasQuestion = true;
    act(() => {
      services.transport.emit("workflow.project", taskQuestionWaitingEvent);
    });

    const question = await screen.findByRole("region", { name: "Question" });
    expect(await within(question).findByRole("radio", { name: /Trail mix/u })).toBeInTheDocument();
    expect(getCallCount(services.transport.calls, "workflow.task.get")).toBeGreaterThanOrEqual(2);
    expect(getCallCount(services.transport.calls, "workflow.task.attention.list")).toBeGreaterThanOrEqual(2);
  });

  it("keeps unsaved title and description edits across a live refresh", async () => {
    let serverBody = taskDetailNoInboxResponse.task.body;
    let serverUpdatedAt = 2;
    const services = mountTaskDetail({
      method: "workflow.task.get",
      handler: () => ({
        task: {
          ...taskDetailNoInboxResponse.task,
          summary: { ...taskDetailNoInboxResponse.task.summary, updated_at_unix_ms: serverUpdatedAt },
          body: serverBody,
        },
      }),
    });

    const description = await screen.findByRole("textbox", { name: "Description" });
    fireEvent.focus(description);
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Half-written notes" },
    });
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();

    serverBody = "Agent rewrote the body";
    serverUpdatedAt = 99;
    act(() => {
      services.transport.emit("workflow.project", taskUpdatedEvent);
    });

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.get")).toBeGreaterThan(1);
    });
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Half-written notes");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
  });

  it("preserves unsaved edits through optimistic assignment and event reconciliation without a detail read", async () => {
    const assignmentUpdate = deferred<unknown>();
    const services = mountTaskDetail(
      taskGetRoute(),
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [{ id: priorityLabelID, name: "Priority" }],
          },
        },
      },
      {
        method: "workflow.task.labels.get",
        handler: (_params, callIndex) => ({
          assignment: {
            task_id: "task-1",
            label_ids: callIndex === 0 ? [] : [priorityLabelID],
          },
        }),
      },
      {
        method: "workflow.task.labels.update",
        handler: async () => assignmentUpdate.promise,
      },
    );
    const title = await screen.findByRole("textbox", { name: "Title" });
    fireEvent.change(title, { target: { value: "Half-written title" } });
    fireEvent.focus(screen.getByRole("textbox", { name: "Description" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Half-written notes" },
    });

    fireEvent.click(await screen.findByRole("button", { name: "Edit labels" }));
    fireEvent.click(await screen.findByRole("button", { name: "Priority" }));

    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Half-written title");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveTextContent("Half-written notes");
    expect(screen.getAllByText("Priority").length).toBeGreaterThan(1);
    expect(getCallCount(services.transport.calls, "workflow.task.get")).toBe(1);

    assignmentUpdate.resolve({
      assignment: {
        task_id: "task-1",
        label_ids: [priorityLabelID],
      },
    });
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.update")).toBe(1);
    });
    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "labels_changed",
          occurred_at_unix_ms: 3,
          primary_entity_id: "task-1",
          project_id: "project-1",
          related_ids: [],
          resource: "task",
          workflow_id: "workflow-1",
        },
      });
    });

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.labels.get")).toBe(2);
    });
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Half-written title");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveTextContent("Half-written notes");
    expect(getCallCount(services.transport.calls, "workflow.task.get")).toBe(1);
  });

  it("follows server title updates on a clean surface without unsaved edits", async () => {
    let serverTitle = "Resolve blocker";
    let serverUpdatedAt = 2;
    const services = mountTaskDetail({
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
    });

    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue("Resolve blocker");

    serverTitle = "Renamed by agent";
    serverUpdatedAt = 99;
    act(() => {
      services.transport.emit("workflow.project", taskUpdatedEvent);
    });

    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Renamed by agent");
    });
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});

type TestRoute = NonNullable<Parameters<typeof createTestServices>[0]>[number];

function mountTaskDetail(...routes: readonly TestRoute[]) {
  window.history.pushState(null, "", "/tasks/task-1");
  const services = createTestServices([
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
    { method: "workflow.task.attention.list", result: { items: [], generated_at_unix_ms: 1 } },
    { method: "workflow.task.activity.list", result: activityResponse },
    ...routes,
  ]);
  render(
    <TestAppProviders services={services}>
      <TaskDetailSurface enabled taskId="task-1" />
    </TestAppProviders>,
  );
  return services;
}

function taskGetRoute(overrides: Partial<typeof taskDetailNoInboxResponse.task> = {}) {
  return {
    method: "workflow.task.get",
    result: { task: { ...taskDetailNoInboxResponse.task, ...overrides } },
  } as const;
}

function makeDescriptionOverflow(description: HTMLElement): void {
  Object.defineProperties(description, {
    clientHeight: { configurable: true, value: 120 },
    scrollHeight: { configurable: true, value: 240 },
  });
  act(() => {
    window.dispatchEvent(new Event("resize"));
  });
}

function renderWithAppProviders(content: ReactNode) {
  const services = createTestServices([
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
  ]);
  const withProviders = (child: ReactNode) => (
    <TestAppProviders services={services}>{child}</TestAppProviders>
  );
  const view = render(withProviders(content));
  return {
    ...view,
    rerender: (next: ReactNode) => {
      view.rerender(withProviders(next));
    },
  };
}
function TaskDetailContentHarness({ detail }: Readonly<{ detail: TaskDetail }>) {
  const activity = useTaskActivity(detail.id, false);
  const attention = useTaskAttention(detail.id, false);
  const comments = useTaskComments(detail.id, false);
  return (
    <ProjectLabelsProvider projectID={detail.projectID}>
      <TaskDetailContentAssignmentHarness detail={detail}>
        <TaskDetailContent
          activity={activity}
          attention={attention}
          comments={comments}
          detail={detail}
          openLink={() => undefined}
        />
      </TaskDetailContentAssignmentHarness>
    </ProjectLabelsProvider>
  );
}

function TaskDetailContentAssignmentHarness({
  children,
  detail,
}: Readonly<{ children: ReactNode; detail: TaskDetail }>) {
  const catalog = useProjectLabelCatalog();
  return (
    <TaskLabelAssignmentProvider
      catalog={catalog.data ?? null}
      taskID={detail.id}
      workflowID={detail.workflowID}
    >
      {children}
    </TaskLabelAssignmentProvider>
  );
}

function VirtualizedDescriptionHarness({ bodyVisible }: Readonly<{ bodyVisible: boolean }>) {
  const [presentation, setPresentation] = useState(initialDescriptionPresentationState);
  const draft: TaskDraft = { body: "Long description", title: "Task" };
  return bodyVisible ? (
    <DescriptionIsland
      disabled={false}
      draft={draft}
      error={null}
      onDraftChange={() => undefined}
      onPresentationChange={setPresentation}
      presentation={presentation}
    />
  ) : null;
}

function installReducedMotionMatchMedia(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({
      addEventListener: vi.fn(),
      addListener: vi.fn(),
      dispatchEvent: vi.fn(),
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      removeEventListener: vi.fn(),
      removeListener: vi.fn(),
    })),
  );
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | null = null;
  let reject: ((error: unknown) => void) | null = null;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return {
    promise,
    reject(error: unknown): void {
      reject?.(error);
    },
    resolve(value: T): void {
      resolve?.(value);
    },
  };
}

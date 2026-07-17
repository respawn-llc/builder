import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, vi } from "vitest";

import type { TaskDetail } from "@/api";
import type { JsonValue } from "@/api";
import { createTestServices, startupRoutes, TestAppProviders } from "@/test-support/app-services";
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
import { useTaskActivity, useTaskComments } from "./useTaskDetailData";

describe("TaskDetailSurface editing", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
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
          const update = taskUpdateParamsSchema.safeParse(params);
          if (update.success) {
            currentTitle = update.data.title ?? currentTitle;
            currentBody = update.data.body ?? currentBody;
          }
          return taskUpdateResponse;
        },
      },
    ]);

    renderTaskDetail(services);

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

    renderTaskDetail(services);

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
    window.history.pushState(null, "", "/tasks/task-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        result: {
          task: {
            ...taskDetailNoInboxResponse.task,
            body: "Long description",
          },
        },
      },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    renderTaskDetail(services);

    const description = await screen.findByRole("textbox", { name: "Description" });
    Object.defineProperties(description, {
      clientHeight: { configurable: true, value: 120 },
      scrollHeight: { configurable: true, value: 240 },
    });
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });

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
    window.history.pushState(null, "", "/tasks/task-1");
    installReducedMotionMatchMedia();
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.task.get",
        result: {
          task: {
            ...taskDetailNoInboxResponse.task,
            body: "Long description",
          },
        },
      },
      { method: "workflow.task.activity.list", result: activityResponse },
    ]);

    renderTaskDetail(services);

    const description = await screen.findByRole("textbox", { name: "Description" });
    Object.defineProperties(description, {
      clientHeight: { configurable: true, value: 120 },
      scrollHeight: { configurable: true, value: 240 },
    });
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });

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
    const services = createTestServices(startupRoutes);
    const { rerender } = render(
      <TestAppProviders services={services}>
        <TaskDetailContentHarness detail={taskOne} />
      </TestAppProviders>,
    );

    fireEvent.focus(await screen.findByRole("textbox", { name: "Description" }));
    expect(screen.getByRole("textbox", { name: "Description" })).toBeInstanceOf(HTMLTextAreaElement);

    rerender(
      <TestAppProviders services={services}>
        <TaskDetailContentHarness detail={taskTwo} />
      </TestAppProviders>,
    );

    const description = await screen.findByRole("textbox", { name: "Description" });
    expect(description).not.toBeInstanceOf(HTMLTextAreaElement);
    Object.defineProperties(description, {
      clientHeight: { configurable: true, value: 120 },
      scrollHeight: { configurable: true, value: 240 },
    });
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(await screen.findByRole("button", { name: "Expand" })).toBeInTheDocument();
  });

  it("retains expanded description presentation when the virtualized body row remounts", async () => {
    const services = createTestServices(startupRoutes);
    const { rerender } = render(
      <TestAppProviders services={services}>
        <VirtualizedDescriptionHarness bodyVisible />
      </TestAppProviders>,
    );

    const description = screen.getByRole("textbox", { name: "Description" });
    Object.defineProperties(description, {
      clientHeight: { configurable: true, value: 120 },
      scrollHeight: { configurable: true, value: 240 },
    });
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    fireEvent.click(await screen.findByRole("button", { name: "Expand" }));

    rerender(
      <TestAppProviders services={services}>
        <VirtualizedDescriptionHarness bodyVisible={false} />
      </TestAppProviders>,
    );
    expect(screen.queryByTestId("task-description-input-frame")).not.toBeInTheDocument();

    rerender(
      <TestAppProviders services={services}>
        <VirtualizedDescriptionHarness bodyVisible />
      </TestAppProviders>,
    );
    expect(screen.getByRole("textbox", { name: "Description" })).not.toBeInstanceOf(HTMLTextAreaElement);
    expect(screen.queryByRole("button", { name: "Expand" })).not.toBeInTheDocument();
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

    renderTaskDetail(services);

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

    renderTaskDetail(services);

    const description = await screen.findByRole("textbox", { name: "Description" });
    fireEvent.focus(description);
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Half-written notes" },
    });
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();

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

    renderTaskDetail(services);

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

function renderTaskDetail(services: ReturnType<typeof createTestServices>) {
  return render(
    <TestAppProviders services={services}>
      <TaskDetailSurface enabled taskId="task-1" />
    </TestAppProviders>,
  );
}

function TaskDetailContentHarness({ detail }: Readonly<{ detail: TaskDetail }>) {
  const activity = useTaskActivity(detail.id, false);
  const comments = useTaskComments(detail.id, false);
  return (
    <TaskDetailContent activity={activity} comments={comments} detail={detail} openLink={() => undefined} />
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

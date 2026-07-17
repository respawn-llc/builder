import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactNode, useState } from "react";
import { afterEach, vi } from "vitest";

import { App } from "../../App";
import type { TaskDetail } from "../../api";
import type { JsonValue } from "../../api/json";
import { taskDetailSchema } from "../../api/schemas/workflowBoard";
import { AppProviders } from "../../app/AppProviders";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";
import {
  activityResponse,
  getCallCount,
  questionAttention,
  taskDetailNoInboxResponse,
  taskDetailResponse,
  taskQuestionWaitingEvent,
  taskUpdateParamsSchema,
  taskUpdateResponse,
  taskUpdatedEvent,
} from "../../testSupport/taskDetailFixtures";
import { TaskDetailContent } from "./TaskDetailContent";
import { initialDescriptionPresentationState } from "./TaskDetailDescriptionPresentation";
import { DescriptionIsland, type TaskDraft } from "./TaskDetailRows";
import { useTaskActivity, useTaskComments } from "./useTaskDetailData";

describe("TaskDetailSurface editing", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
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
    const taskOne: TaskDetail = taskDetailSchema.parse(taskDetailResponse);
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
            attention: hasQuestion ? [questionAttention] : [],
          },
        }),
      },
      { method: "ask.listPendingBySession", result: { Asks: [] } },
    );

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
      services.transport.emit("workflow.event", taskUpdatedEvent);
    });

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.get")).toBeGreaterThan(1);
    });
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Half-written notes");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
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
      services.transport.emit("workflow.event", taskUpdatedEvent);
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
    ...routes,
    { method: "workflow.task.activity.list", result: activityResponse },
  ]);
  render(<App services={services} />);
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
  const services = createTestServices(startupRoutes);
  const withProviders = (child: ReactNode) => <AppProviders services={services}>{child}</AppProviders>;
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

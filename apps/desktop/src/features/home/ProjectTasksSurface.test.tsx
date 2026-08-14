import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";

import {
  type ProjectTaskGroupCounts,
  type ProjectTaskGroupDefinition,
  type TaskListItem,
} from "@/api";
import { queryKeys, type SidebarDestination, type SidebarRootController } from "@/app-facade";
import type * as ProjectTaskListData from "./projectTaskListData";

import { appI18n, initializeI18n } from "@/i18n";
import { createProjectTasksViewMemory } from "./projectTasksViewMemory";

const projectTaskGroupDefinitions: readonly ProjectTaskGroupDefinition[] = [
  {
    group: "active",
    statusKinds: ["waiting_question", "waiting_approval", "interrupted", "running", "queued", "active"],
  },
  { group: "backlog", statusKinds: ["backlog"] },
  { group: "done", statusKinds: ["done"] },
];

type ActiveTaskDestination = Readonly<{
  kind: "taskDetail";
  mode?: "overlay" | "shift";
  taskID: string;
}>;

const fixture = vi.hoisted<{
  activeDestination: ActiveTaskDestination | null;
  board: {
    isError: boolean;
    isPending: boolean;
    isSuccess: boolean;
    data: {
      workflows: readonly Readonly<{
        id: string;
        name: string;
        description: string | null;
        isProjectDefault: boolean;
      }>[];
    };
    error: Error | null;
    refetch: ReturnType<typeof vi.fn>;
  };
  counts: ProjectTaskGroupCounts["counts"];
  countsError: Error | null;
  countsEstablished: boolean;
  countsPending: boolean;
  initialGroupPagesError: boolean;
  initialGroupPagesEstablished: boolean;
  initialGroupPagesRefreshing: boolean;
  backlogTasks: readonly TaskListItem[];
  doneDataOverrides: Partial<ProjectTaskListData.ProjectTaskGroupData>;
  invalidations: unknown[];
  open: ReturnType<typeof vi.fn<SidebarRootController["open"]>>;
  labelCatalogRequests: number;
  projectSubscriptions: number;
  assignmentRequests: number;
}>(() => ({
  activeDestination: null,
  board: {
    isError: false,
    isPending: false,
    isSuccess: true,
    data: {
      workflows: [
        {
          id: "workflow-1",
          name: "Delivery",
          description: "Ship work",
          isProjectDefault: true,
        },
        {
          id: "workflow-2",
          name: "Support",
          description: null,
          isProjectDefault: false,
        },
      ],
    },
    error: null,
    refetch: vi.fn(),
  },
  counts: { active: 2, backlog: 1, done: 1 },
  countsError: null,
  countsEstablished: true,
  countsPending: false,
  initialGroupPagesError: false,
  initialGroupPagesEstablished: true,
  initialGroupPagesRefreshing: false,
  backlogTasks: [task("backlog-1", "KNT-3", "Backlog task")],
  doneDataOverrides: {},
  invalidations: [],
  open: vi.fn<SidebarRootController["open"]>(() => ({
    lifecycle: Promise.resolve("closed"),
    release: vi.fn(),
  })),
  labelCatalogRequests: 0,
  projectSubscriptions: 0,
  assignmentRequests: 0,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject: vi.fn() }),
  useAppServices: () => ({
    api: {
      listProjectWorkflowLinks: async (projectID: string) =>
        fixture.board.data.workflows.map((workflow, index) => ({
          id: `link-${index.toString()}`,
          projectID,
          workflowID: workflow.id,
          isDefault: workflow.isProjectDefault,
        })),
      getWorkflow: async (workflowID: string) => {
        const workflow = fixture.board.data.workflows.find((candidate) => candidate.id === workflowID);
        if (workflow === undefined) {
          throw new Error(`Missing Workflow ${workflowID}.`);
        }
        return {
          workflow: {
            id: workflow.id,
            name: workflow.name,
            description: workflow.description ?? "",
            version: 1,
          },
        };
      },
      getTaskLabels: async (taskID: string) => {
        fixture.assignmentRequests += 1;
        return { taskID, labelIDs: [] };
      },
      listProjectLabels: async () => {
        fixture.labelCatalogRequests += 1;
        return {
          projectID: "project-1",
          labels: [{ id: "label-1", name: "Priority" }],
        };
      },
      subscribeProject: () => {
        fixture.projectSubscriptions += 1;
        return {
          close() {
            fixture.projectSubscriptions -= 1;
          },
        };
      },
    },
    logger: { append: vi.fn() },
    nativeBridge: { capabilities: { platform: "macos" } },
    storageNamespace: null,
  }),
  useConnectionSnapshot: () => ({ generation: 1, phase: "connected" }),
  useOwnedSidebarRoots: () => ({ open: fixture.open }),
  useSidebarShell: () => ({ activeDestination: fixture.activeDestination }),
  useStatusController: () => ({ dismiss: vi.fn(), push: vi.fn() }),
}));

vi.mock("./projectTaskListData", async (importOriginal) => {
  const actual = await importOriginal<typeof ProjectTaskListData>();
  return {
    ...actual,
    useProjectTaskListEvents: () => undefined,
    useProjectTaskListData: () => ({
      counts: countsQuery(
        fixture.counts,
        fixture.countsError,
        fixture.countsPending,
        fixture.countsEstablished,
      ),
      active: groupData(
        mockedActiveTasks ?? [
          task("active-1", "KNT-1", "Active task"),
          task("active-2", "KNT-2", "Running task"),
        ],
        fixture.initialGroupPagesEstablished,
        fixture.initialGroupPagesError,
        fixture.initialGroupPagesRefreshing,
      ),
      backlog: groupData(
        fixture.backlogTasks,
        fixture.initialGroupPagesEstablished,
        fixture.initialGroupPagesError,
        fixture.initialGroupPagesRefreshing,
      ),
      done: {
        ...groupData([task("done-1", "KNT-4", "Done task")]),
        ...fixture.doneDataOverrides,
      },
    }),
  };
});

import { ProjectTasksSurface } from "./ProjectTasksSurface";

beforeAll(async () => initializeI18n());

describe("ProjectTasksSurface", () => {
  beforeEach(() => {
    fixture.board.data = {
      workflows: [
        {
          id: "workflow-1",
          name: "Delivery",
          description: "Ship work",
          isProjectDefault: true,
        },
        {
          id: "workflow-2",
          name: "Support",
          description: null,
          isProjectDefault: false,
        },
      ],
    };
    fixture.counts = { active: 2, backlog: 1, done: 1 };
    fixture.countsError = null;
    fixture.countsEstablished = true;
    fixture.countsPending = false;
    fixture.initialGroupPagesError = false;
    fixture.initialGroupPagesEstablished = true;
    fixture.initialGroupPagesRefreshing = false;
    fixture.backlogTasks = [task("backlog-1", "KNT-3", "Backlog task")];
    fixture.doneDataOverrides = {};
    fixture.activeDestination = null;
    fixture.open.mockReset();
    fixture.invalidations = [];
    fixture.labelCatalogRequests = 0;
    fixture.projectSubscriptions = 0;
    fixture.assignmentRequests = 0;
    mockedActiveTasks = undefined;
  });

  it("opens the server-defined Status legend from keyboard focus", async () => {
    renderSurface();

    const statusLegendTriggers = screen.getAllByRole("button", { name: appI18n.t("task.status") });
    fireEvent.focus(statusLegendTriggers[0]);

    expect(await screen.findByRole("tooltip")).toBeVisible();
  });

  it("hides zero-count groups and opens Project-scoped New Task when the Project has no Tasks", () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const view = renderSurface();

    expect(screen.queryByRole("columnheader")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("board.newTask") }));
    const destination = openedDestination();
    expect(destination).toMatchObject({
      boardQueryWorkflowID: undefined,
      kind: "newTask",
      mode: "shift",
      projectID: "project-1",
    });
    if (destination.kind !== "newTask") throw new Error("Expected New Task destination.");
    expect(destination.onCreated).toBeTypeOf("function");

    fixture.counts = { active: 2, backlog: 0, done: 0 };
    view.rerender(withQueryClient(surface()));
    expect(screen.getAllByRole("button", { expanded: true })).toHaveLength(1);
  });

  it("offers Link Workflow instead of New Task when multiple links have no default", () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    fixture.board.data = {
      workflows: fixture.board.data.workflows.map((workflow) => ({
        ...workflow,
        isProjectDefault: false,
      })),
    };

    renderSurface();
    const linkActions = screen.getAllByRole("button", { name: appI18n.t("workflowLibrary.linkWorkflow") });
    const emptyStateLinkAction = linkActions.at(-1);
    if (emptyStateLinkAction === undefined) {
      throw new Error("Expected the no-Task empty state Link Workflow action.");
    }
    fireEvent.click(emptyStateLinkAction);
    const destination = openedDestination();
    expect(destination).toMatchObject({
      kind: "linkWorkflow",
      mode: "shift",
      projectID: "project-1",
    });
    if (destination.kind !== "linkWorkflow") throw new Error("Expected Link Workflow destination.");
    expect(destination.onCompleted).toBeTypeOf("function");
    expect(screen.queryByRole("button", { name: appI18n.t("board.newTask") })).not.toBeInTheDocument();
  });

  it("offers Project-scoped New Task for a sole linked Workflow without making Desktop select it", () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    fixture.board.data = {
      workflows: [
        {
          id: "workflow-1",
          name: "Delivery",
          description: "Ship work",
          isProjectDefault: false,
        },
      ],
    };

    renderSurface();
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("board.newTask") }));
    const destination = openedDestination();
    expect(destination).toMatchObject({
      boardQueryWorkflowID: undefined,
      kind: "newTask",
      mode: "shift",
      projectID: "project-1",
    });
    if (destination.kind !== "newTask") throw new Error("Expected New Task destination.");
    expect(destination.onCreated).toBeTypeOf("function");
  });

  it("keeps Tasks-origin Link Workflow on Tasks and refreshes its authoritative projections", async () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    renderSurface();
    const linkActions = screen.getAllByRole("button", { name: appI18n.t("workflowLibrary.linkWorkflow") });
    const linkAction = linkActions.at(-1);
    if (linkAction === undefined) throw new Error("Expected a Link Workflow action.");
    fireEvent.click(linkAction);
    const destination = openedDestination();
    if (destination.kind !== "linkWorkflow") throw new Error("Expected Link Workflow destination.");

    await act(async () => {
      await destination.onCompleted({ kind: "linked", workflowID: "workflow-3" });
    });

    expect(fixture.invalidations).toEqual(
      expect.arrayContaining([
        {
          queryKey: queryKeys.projectBoardsRoot("project-1"),
          refetchType: "active",
        },
        {
          queryKey: queryKeys.projectTaskListsRoot("project-1"),
          refetchType: "active",
        },
      ]),
    );
  });

  it("preserves collapsed Backlog after New Task success and exposes no persistent New Task action", async () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const memory = createProjectTasksViewMemory();
    memory.setDisclosure({ active: true, backlog: false, done: false });
    const view = renderSurface(memory);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("board.newTask") }));
    const destination = openedDestination();
    if (destination.kind !== "newTask") throw new Error("Expected New Task destination.");

    await act(async () => {
      await destination.onCreated?.("task-created");
    });

    expect(memory.read().disclosure.backlog).toBe(false);

    fixture.counts = { active: 1, backlog: 0, done: 0 };
    view.rerender(withQueryClient(surface(memory)));
    expect(screen.queryByRole("button", { name: appI18n.t("board.newTask") })).not.toBeInTheDocument();
  });

  it("retains disclosure in workspace view memory", () => {
    const memory = createProjectTasksViewMemory();
    const view = renderSurface(memory);

    const activeGroupName = appI18n.t("home.prototype.taskGroupCount", { count: 2, group: appI18n.t("home.prototype.statusGroups.active") });
    const activeGroup = screen.getByRole("button", { name: activeGroupName });
    fireEvent.click(activeGroup);
    expect(activeGroup).toHaveAttribute("aria-expanded", "false");
    view.unmount();

    renderSurface(memory);
    expect(screen.getByRole("button", { name: activeGroupName })).toHaveAttribute("aria-expanded", "false");
  });

  it("restores retained pixels after remounted group pages establish their first results", () => {
    const originalScrollTop = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollTop");
    const scrollPositions = new WeakMap<HTMLElement, number>();
    let maximumScrollTop = Number.POSITIVE_INFINITY;
    Object.defineProperty(HTMLElement.prototype, "scrollTop", {
      configurable: true,
      get: function getScrollTop(this: HTMLElement) {
        return scrollPositions.get(this) ?? 0;
      },
      set: function setScrollTop(this: HTMLElement, value: number) {
        scrollPositions.set(this, Math.min(value, maximumScrollTop));
      },
    });
    try {
      const memory = createProjectTasksViewMemory();
      const { unmount } = renderSurface(memory);
      const initialGrid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
      initialGrid.scrollTop = 200;
      initialGrid.scrollLeft = 120;
      fireEvent.scroll(initialGrid);
      unmount();

      fixture.initialGroupPagesEstablished = false;
      maximumScrollTop = 0;
      const view = renderSurface(memory);
      const loadingGrid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
      expect(loadingGrid.scrollTop).toBe(0);
      expect(loadingGrid.scrollLeft).toBe(0);
      fireEvent.scroll(loadingGrid);
      expect(memory.read()).toMatchObject({
        horizontalOffsetPx: 0,
        verticalOffsetPx: 200,
      });

      fixture.initialGroupPagesError = true;
      view.rerender(withQueryClient(surface(memory)));
      expect(screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") }).scrollTop).toBe(0);

      fixture.initialGroupPagesError = false;
      fixture.initialGroupPagesEstablished = true;
      maximumScrollTop = 1_000;
      view.rerender(withQueryClient(surface(memory)));

      const restoredGrid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
      expect(restoredGrid.scrollTop).toBe(200);
      expect(restoredGrid.scrollLeft).toBe(0);
      fireEvent.scroll(restoredGrid);
      expect(memory.read()).toMatchObject({
        horizontalOffsetPx: 0,
        verticalOffsetPx: 200,
      });
    } finally {
      if (originalScrollTop === undefined) {
        Reflect.deleteProperty(HTMLElement.prototype, "scrollTop");
      } else {
        Object.defineProperty(HTMLElement.prototype, "scrollTop", originalScrollTop);
      }
    }
  });

  it("restores retained pixels while established group pages refresh", () => {
    const memory = createProjectTasksViewMemory();
    memory.setScrollOffsets(200, 120);
    fixture.initialGroupPagesRefreshing = true;

    renderSurface(memory);

    const grid = screen.getByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") });
    expect(grid.scrollTop).toBe(200);
    expect(grid.scrollLeft).toBe(0);
  });

  it("replaces the complete Tasks surface when initial exact counts fail", () => {
    const countsError = new Error("Counts unavailable");
    fixture.countsError = countsError;
    fixture.countsEstablished = false;

    renderSurface();

    expect(screen.getByRole("alert")).toHaveTextContent(countsError.message);
    expect(screen.getByRole("button", { name: appI18n.t("app.retry") })).toBeInTheDocument();
    expect(screen.queryByRole("grid", { name: appI18n.t("home.prototype.projectTasksGrid") })).not.toBeInTheDocument();
  });

  it("opens Task Detail with the containing sidebar mode", () => {
    renderSurface(createProjectTasksViewMemory(), "overlay", [task("active-1", "KNT-1", "Task")]);

    fireEvent.click(screen.getByRole("row", { name: "KNT-1 Task" }));
    expect(fixture.open).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      taskID: "active-1",
    });
  });

  it("opens Task Detail from the dependency chip and uses the active sidebar destination for selection", () => {
    fixture.activeDestination = { kind: "taskDetail", mode: "shift", taskID: "active-1" };
    renderSurface(createProjectTasksViewMemory(), "overlay", [
      task("active-1", "KNT-1", "Selected task", {
        dependencyProgress: { satisfiedCount: 2, totalCount: 2 },
      }),
    ]);

    const row = screen.getByRole("row", { name: "KNT-1 Selected task" });
    expect(row).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("task.dependenciesProgressAccessible", { completed: 2, total: 2 }) }));
    expect(fixture.open).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "shift",
      taskID: "active-1",
    });

    fireEvent.keyDown(row, { key: "Enter" });
    fireEvent.keyDown(row, { key: " " });
    expect(fixture.open).toHaveBeenCalledTimes(3);
  });

  it("keeps Labels subscribed and applies last-wins selection while the chooser is open", async () => {
    fixture.activeDestination = { kind: "taskDetail", mode: "shift", taskID: "active-1" };
    renderSurface(createProjectTasksViewMemory(), "shift", [
      task("active-1", "KNT-1", "Detail selected"),
      task("active-2", "KNT-2", "Labels selected", {
        labels: [{ id: "label-1", name: "Priority" }],
      }),
    ]);

    const detailRow = screen.getByRole("row", { name: "KNT-1 Detail selected" });
    const labelsRow = screen.getByRole("row", { name: "KNT-2 Labels selected" });
    expect(detailRow).toHaveAttribute("aria-selected", "true");
    expect(labelsRow).toHaveAttribute("aria-selected", "false");

    expect(fixture.labelCatalogRequests).toBe(0);
    expect(fixture.assignmentRequests).toBe(0);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("home.prototype.editTaskLabels", { shortID: "KNT-2" }) }));
    expect(fixture.open).not.toHaveBeenCalled();
    expect(detailRow).toHaveAttribute("aria-selected", "false");
    expect(labelsRow).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("textbox", { name: appI18n.t("labels.search") })).toBeInTheDocument();
    await waitFor(() => {
      expect(fixture.labelCatalogRequests).toBe(1);
      expect(fixture.assignmentRequests).toBe(1);
      expect(fixture.projectSubscriptions).toBe(1);
      expect(screen.getByRole("dialog")).toHaveAttribute("data-side", "top");
    });

    fireEvent.keyDown(screen.getByRole("textbox", { name: appI18n.t("labels.search") }), {
      key: "Escape",
    });
    expect(detailRow).toHaveAttribute("aria-selected", "true");
    expect(labelsRow).toHaveAttribute("aria-selected", "false");
    expect(fixture.labelCatalogRequests).toBe(1);
    expect(fixture.assignmentRequests).toBe(1);
    expect(fixture.projectSubscriptions).toBe(0);
  });
});

function renderSurface(
  memory = createProjectTasksViewMemory(),
  sidebarMode: "overlay" | "shift" = "shift",
  activeTasks?: readonly TaskListItem[],
) {
  if (activeTasks !== undefined) {
    fixture.counts = { active: activeTasks.length, backlog: 0, done: 0 };
    mockedActiveTasks = activeTasks;
  }
  return render(withQueryClient(surface(memory, sidebarMode)));
}

function surface(memory = createProjectTasksViewMemory(), sidebarMode: "overlay" | "shift" = "shift") {
  return (
    <I18nextProvider i18n={appI18n}>
      <ProjectTasksSurface projectID="project-1" sidebarMode={sidebarMode} viewMemory={memory} />
    </I18nextProvider>
  );
}

function withQueryClient(children: React.ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  vi.spyOn(queryClient, "invalidateQueries").mockImplementation(async (filters) => {
    fixture.invalidations.push(filters);
  });
  queryClient.setQueryData(
    queryKeys.projectTaskWorkflows("project-1"),
    fixture.board.data.workflows,
  );
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function openedDestination(): SidebarDestination {
  const destination = fixture.open.mock.lastCall?.[0];
  if (destination === undefined) throw new Error("Expected a Sidebar destination to open.");
  return destination;
}

function countsQuery(
  counts: ProjectTaskGroupCounts["counts"],
  error: Error | null,
  pending: boolean,
  established: boolean,
) {
  return {
    data: established
      ? { projectID: "project-1", definitions: projectTaskGroupDefinitions, counts, generatedAt: 1 }
      : undefined,
    error,
    isError: error !== null,
    isFetching: false,
    isPending: pending,
    refetch: vi.fn(),
  };
}

function groupData(
  tasks: readonly TaskListItem[],
  established = true,
  initialError = false,
  refreshing = false,
) {
  const establishedTasks = established ? tasks : [];
  return {
    error: initialError ? new Error("Group unavailable") : null,
    fetchNextPage: vi.fn(),
    fetchPreviousPage: vi.fn(),
    hasNextPage: false,
    hasPreviousPage: false,
    isError: initialError,
    isFetchNextPageError: false,
    isFetchPreviousPageError: false,
    isFetching: refreshing,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: !established && !initialError,
    nextRequestGeneration: "project-1:end",
    pages: established
      ? [
          {
            scope: { projectID: "project-1", workflowID: null },
            matchingWorkflowCardinality: "multiple" as const,
            nextOffset: null,
            generatedAt: 1,
            tasks: establishedTasks,
          },
        ]
      : [],
    previousRequestGeneration: "project-1:0",
    refetch: vi.fn(),
    tasks: establishedTasks,
  };
}

let mockedActiveTasks: readonly TaskListItem[] | undefined;

function task(
  id: string,
  shortID: string,
  title: string,
  overrides: Partial<TaskListItem> = {},
): TaskListItem {
  return {
    id,
    shortID,
    workflowID: "workflow-1",
    workflowName: "Delivery",
    title,
    createdAt: 1,
    updatedAt: 1,
    columnKeys: null,
    status: { kind: "active", nativeState: "active", nodeIDs: [], attentionTypes: [] },
    labels: [],
    dependencyProgress: null,
    ...overrides,
  };
}

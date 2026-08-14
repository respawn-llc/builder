import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";

import { canonicalBoardFilter, type ProjectTaskGroupCounts, type TaskListItem } from "@/api";
import { queryKeys, type SidebarDestination, type SidebarRootController } from "@/app-facade";
import type * as ProjectTaskListData from "./projectTaskListData";

import { appI18n, initializeI18n } from "@/i18n";
import { createProjectTasksViewMemory } from "./projectTasksViewMemory";

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
  backlogTasks: readonly TaskListItem[];
  doneDataOverrides: Partial<ProjectTaskListData.ProjectTaskGroupData>;
  invalidations: unknown[];
  open: ReturnType<typeof vi.fn<SidebarRootController["open"]>>;
  labelCatalogRequests: number;
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
  backlogTasks: [taskFixture("backlog-1", "KNT-3", "Backlog task")],
  doneDataOverrides: {},
  invalidations: [],
  open: vi.fn<SidebarRootController["open"]>(() => ({
    lifecycle: Promise.resolve("closed"),
    release: vi.fn(),
  })),
  labelCatalogRequests: 0,
  assignmentRequests: 0,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject: vi.fn() }),
  useAppServices: () => ({
    api: {
      getBoard: async () => fixture.board.data,
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
    },
    logger: { append: vi.fn() },
    nativeBridge: { capabilities: { platform: "macos" } },
  }),
  useConnectionSnapshot: () => ({ generation: 1, phase: "disconnected" }),
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
      ),
      backlog: groupData(fixture.backlogTasks),
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
    fixture.backlogTasks = [task("backlog-1", "KNT-3", "Backlog task")];
    fixture.doneDataOverrides = {};
    fixture.activeDestination = null;
    fixture.open.mockReset();
    fixture.invalidations = [];
    fixture.labelCatalogRequests = 0;
    fixture.assignmentRequests = 0;
    mockedActiveTasks = undefined;
  });

  it("renders the workflow strip and the ordered grouped Task island with default disclosure", () => {
    renderSurface();

    expect(screen.getByRole("button", { name: "Delivery" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Support" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link workflow" })).toBeInTheDocument();
    expect(screen.getByRole("grid", { name: "Project tasks" })).toBeInTheDocument();
    expect(
      screen
        .getAllByRole("columnheader")
        .map((header) => header.getAttribute("aria-label") ?? header.textContent),
    ).toEqual(["Status", "Dependencies", "ID", "Title", "Workflow", "Labels"]);
    const groups = screen.getAllByRole("button", { name: /tasks?$/ });
    expect(groups.map((group) => group.textContent)).toEqual([
      "Active 2Active, 2 tasks",
      "Backlog 1Backlog, 1 task",
      "Done 1Done, 1 task",
    ]);
    expect(groups.map((group) => group.getAttribute("aria-expanded"))).toEqual(["true", "true", "false"]);
    expect(screen.getByRole("row", { name: "KNT-1 Active task" })).toBeInTheDocument();
    expect(screen.queryByRole("row", { name: "KNT-4 Done task" })).not.toBeInTheDocument();
  });

  it("hides zero-count groups and opens Project-scoped New Task when the Project has no Tasks", () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const view = renderSurface();

    expect(screen.queryByRole("columnheader")).not.toBeInTheDocument();
    expect(screen.getByText("No tasks yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "New Task" }));
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
    expect(screen.getByRole("button", { name: "Active, 2 tasks" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Backlog/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Done/ })).not.toBeInTheDocument();
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
    const linkActions = screen.getAllByRole("button", { name: "Link workflow" });
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
    expect(screen.queryByRole("button", { name: "New Task" })).not.toBeInTheDocument();
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
    fireEvent.click(screen.getByRole("button", { name: "New Task" }));
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
    const linkActions = screen.getAllByRole("button", { name: "Link workflow" });
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

  it("reveals authoritative Backlog after New Task success without opening Task Detail", async () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const memory = createProjectTasksViewMemory();
    const view = renderSurface(memory);
    fireEvent.click(screen.getByRole("button", { name: "New Task" }));
    const destination = openedDestination();
    if (destination.kind !== "newTask") throw new Error("Expected New Task destination.");
    fixture.open.mockClear();

    await act(async () => {
      await destination.onCreated?.("task-created");
    });

    expect(memory.read().disclosure.backlog).toBe(true);
    expect(fixture.invalidations).toContainEqual({
      queryKey: queryKeys.projectTaskListsRoot("project-1"),
      refetchType: "active",
    });
    expect(fixture.open).not.toHaveBeenCalled();

    fixture.counts = { active: 0, backlog: 1, done: 0 };
    fixture.backlogTasks = [task("task-created", "KNT-5", "Created task")];
    view.rerender(withQueryClient(surface(memory)));
    expect(screen.getByRole("row", { name: "KNT-5 Created task" })).toBeInTheDocument();
  });

  it("preserves collapsed Backlog after New Task success and exposes no persistent New Task action", async () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const memory = createProjectTasksViewMemory();
    memory.setDisclosure({ active: true, backlog: false, done: false });
    const view = renderSurface(memory);
    fireEvent.click(screen.getByRole("button", { name: "New Task" }));
    const destination = openedDestination();
    if (destination.kind !== "newTask") throw new Error("Expected New Task destination.");

    await act(async () => {
      await destination.onCreated?.("task-created");
    });

    expect(memory.read().disclosure.backlog).toBe(false);

    fixture.counts = { active: 1, backlog: 0, done: 0 };
    view.rerender(withQueryClient(surface(memory)));
    expect(screen.queryByRole("button", { name: "New Task" })).not.toBeInTheDocument();
  });

  it("retains disclosure in workspace view memory", () => {
    const memory = createProjectTasksViewMemory();
    const view = renderSurface(memory);

    fireEvent.click(screen.getByRole("button", { name: "Active, 2 tasks" }));
    expect(screen.getByRole("button", { name: "Active, 2 tasks" })).toHaveAttribute("aria-expanded", "false");
    view.unmount();

    renderSurface(memory);
    expect(screen.getByRole("button", { name: "Active, 2 tasks" })).toHaveAttribute("aria-expanded", "false");
  });

  it("replaces the complete Tasks surface when initial exact counts fail", () => {
    fixture.countsError = new Error("Counts unavailable");
    fixture.countsEstablished = false;

    renderSurface();

    expect(screen.getByRole("alert")).toHaveTextContent("Counts unavailable");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delivery" })).not.toBeInTheDocument();
    expect(screen.queryByRole("grid", { name: "Project tasks" })).not.toBeInTheDocument();
  });

  it("retains rows and exact counts with retry feedback when count refresh fails", () => {
    fixture.countsError = new Error("Counts refresh unavailable");

    renderSurface();

    expect(screen.getByRole("grid", { name: "Project tasks" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Active, 2 tasks" })).toBeInTheDocument();
    expect(screen.getByRole("row", { name: "KNT-1 Active task" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Counts refresh unavailable");
  });

  it("renders canonical six-column content and opens Task Detail with the containing sidebar mode", () => {
    const view = renderSurface(createProjectTasksViewMemory(), "overlay", [
      task("active-1", "KNT-LONG-1001", "Canonical row", {
        dependencyProgress: { satisfiedCount: 1, totalCount: 3 },
        labels: [
          { id: "label-1", name: "Priority" },
          { id: "label-2", name: "Frontend" },
        ],
        status: { kind: "running", nativeState: "running", nodeIDs: [], attentionTypes: [] },
        workflowName: "Very long delivery workflow",
      }),
    ]);

    expect(screen.getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Dependencies" })).toBeInTheDocument();
    expect(screen.getByTestId("project-task-status-active-1")).toContainHTML("<svg");
    expect(screen.getByRole("progressbar", { name: "Dependencies: 1 of 3 complete." })).toBeInTheDocument();
    expect(screen.getByTestId("project-task-id-active-1")).toHaveAttribute("title", "KNT-LONG-1001");
    expect(screen.getByTestId("project-task-id-active-1")).toHaveClass("text-ellipsis", "[direction:rtl]");
    expect(screen.getByTestId("project-task-workflow-active-1")).toHaveClass("truncate");
    expect(screen.getByRole("group", { name: "Labels" })).toHaveTextContent("Priority");
    expect(screen.getByRole("group", { name: "Labels" })).toHaveTextContent("Frontend");

    fireEvent.click(screen.getByRole("row", { name: "KNT-LONG-1001 Canonical row" }));
    expect(fixture.open).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      taskID: "active-1",
    });

    fixture.activeDestination = { kind: "taskDetail", mode: "overlay", taskID: "active-1" };
    view.rerender(withQueryClient(surface(createProjectTasksViewMemory(), "shift")));
    fireEvent.click(screen.getByRole("row", { name: "KNT-LONG-1001 Canonical row" }));
    expect(fixture.open).toHaveBeenLastCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      taskID: "active-1",
    });

    fixture.activeDestination = null;
    view.rerender(withQueryClient(surface(createProjectTasksViewMemory(), "shift")));
    fireEvent.click(screen.getByRole("row", { name: "KNT-LONG-1001 Canonical row" }));
    expect(fixture.open).toHaveBeenLastCalledWith({
      kind: "taskDetail",
      mode: "shift",
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
    fireEvent.click(screen.getByRole("button", { name: "Dependencies: 2 of 2 complete." }));
    expect(fixture.open).toHaveBeenCalledWith({
      kind: "taskDetail",
      mode: "shift",
      taskID: "active-1",
    });

    fireEvent.keyDown(row, { key: "Enter" });
    fireEvent.keyDown(row, { key: " " });
    expect(fixture.open).toHaveBeenCalledTimes(3);
  });

  it("keeps Labels as the only cell action and applies last-wins selection while it is open", async () => {
    fixture.activeDestination = { kind: "taskDetail", mode: "shift", taskID: "active-1" };
    renderSurface(createProjectTasksViewMemory(), "shift", [
      task("active-1", "KNT-1", "Detail selected"),
      task("active-2", "KNT-2", "Labels selected", {
        labels: [{ id: "label-1", name: "Priority" }],
      }),
    ]);

    const detailRow = screen.getByRole("row", { name: "KNT-1 Detail selected" });
    const labelsRow = screen.getByRole("row", { name: "KNT-2 Labels selected" });
    expect(within(detailRow).getAllByRole("gridcell")[1]).toBeEmptyDOMElement();
    expect(detailRow).toHaveAttribute("aria-selected", "true");
    expect(labelsRow).toHaveAttribute("aria-selected", "false");

    expect(fixture.labelCatalogRequests).toBe(0);
    expect(fixture.assignmentRequests).toBe(0);
    fireEvent.click(screen.getByRole("button", { name: "Edit labels for KNT-2" }));
    expect(fixture.open).not.toHaveBeenCalled();
    expect(detailRow).toHaveAttribute("aria-selected", "false");
    expect(labelsRow).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("textbox", { name: "Search or create labels" })).toBeInTheDocument();
    await waitFor(() => {
      expect(fixture.labelCatalogRequests).toBe(1);
      expect(fixture.assignmentRequests).toBe(1);
    });

    fireEvent.keyDown(screen.getByRole("textbox", { name: "Search or create labels" }), {
      key: "Escape",
    });
    expect(detailRow).toHaveAttribute("aria-selected", "true");
    expect(labelsRow).toHaveAttribute("aria-selected", "false");
    expect(fixture.labelCatalogRequests).toBe(1);
    expect(fixture.assignmentRequests).toBe(1);
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
    queryKeys.board("project-1", undefined, canonicalBoardFilter({ kind: "none" })),
    fixture.board.data,
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
    data: established ? { projectID: "project-1", counts, generatedAt: 1 } : undefined,
    error,
    isError: error !== null,
    isFetching: false,
    isPending: pending,
    refetch: vi.fn(),
  };
}

function groupData(tasks: readonly TaskListItem[]) {
  return {
    error: null,
    fetchNextPage: vi.fn(),
    fetchPreviousPage: vi.fn(),
    hasNextPage: false,
    hasPreviousPage: false,
    isError: false,
    isFetchNextPageError: false,
    isFetchPreviousPageError: false,
    isFetching: false,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: false,
    nextRequestGeneration: "project-1:end",
    pages: [],
    previousRequestGeneration: "project-1:0",
    refetch: vi.fn(),
    tasks,
  };
}

function taskFixture(id: string, shortID: string, title: string): TaskListItem {
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

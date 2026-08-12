import { fireEvent, render, screen, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";

import type { ProjectTaskGroupCounts, TaskListItem } from "@/api";
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
    data: { workflows: readonly Readonly<{ id: string; name: string; description: string | null }>[] };
    error: Error | null;
    refetch: ReturnType<typeof vi.fn>;
  };
  counts: ProjectTaskGroupCounts["counts"];
  open: ReturnType<typeof vi.fn>;
}>(() => ({
  activeDestination: null,
  board: {
    isError: false,
    isPending: false,
    isSuccess: true,
    data: {
      workflows: [
        { id: "workflow-1", name: "Delivery", description: "Ship work" },
        { id: "workflow-2", name: "Support", description: null },
      ],
    },
    error: null,
    refetch: vi.fn(),
  },
  counts: { active: 2, backlog: 1, done: 1 },
  open: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => fixture.board,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject: vi.fn() }),
  useAppServices: () => ({ api: {} }),
  useConnectionSnapshot: () => ({ generation: 1, phase: "disconnected" }),
  useOwnedSidebarRoots: () => ({ open: fixture.open }),
  useSidebarShell: () => ({ activeDestination: fixture.activeDestination }),
}));

vi.mock("./projectTaskListData", async (importOriginal) => {
  const actual = await importOriginal<typeof ProjectTaskListData>();
  return {
    ...actual,
    useProjectTaskListEvents: () => undefined,
    useProjectTaskListData: () => ({
      counts: countsQuery(fixture.counts),
      active: groupData(
        mockedActiveTasks ?? [
          task("active-1", "KNT-1", "Active task"),
          task("active-2", "KNT-2", "Running task"),
        ],
      ),
      backlog: groupData([task("backlog-1", "KNT-3", "Backlog task")]),
      done: groupData([task("done-1", "KNT-4", "Done task")]),
    }),
  };
});

import { ProjectTasksSurface } from "./ProjectTasksSurface";

beforeAll(async () => initializeI18n());

describe("ProjectTasksSurface", () => {
  beforeEach(() => {
    fixture.counts = { active: 2, backlog: 1, done: 1 };
    fixture.activeDestination = null;
    fixture.open.mockReset();
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

  it("hides zero-count groups and replaces the grid contents when the Project has no Tasks", () => {
    fixture.counts = { active: 0, backlog: 0, done: 0 };
    const view = renderSurface();

    expect(screen.queryByRole("columnheader")).not.toBeInTheDocument();
    expect(screen.getByText("No tasks yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Task" })).toBeDisabled();

    fixture.counts = { active: 2, backlog: 0, done: 0 };
    view.rerender(surface());
    expect(screen.getByRole("button", { name: "Active, 2 tasks" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Backlog/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Done/ })).not.toBeInTheDocument();
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
    view.rerender(surface(createProjectTasksViewMemory(), "shift"));
    fireEvent.click(screen.getByRole("row", { name: "KNT-LONG-1001 Canonical row" }));
    expect(fixture.open).toHaveBeenLastCalledWith({
      kind: "taskDetail",
      mode: "overlay",
      taskID: "active-1",
    });

    fixture.activeDestination = null;
    view.rerender(surface(createProjectTasksViewMemory(), "shift"));
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

  it("keeps Labels as the only cell action and applies last-wins selection while it is open", () => {
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

    fireEvent.click(screen.getByRole("button", { name: "Edit labels for KNT-2" }));
    expect(fixture.open).not.toHaveBeenCalled();
    expect(detailRow).toHaveAttribute("aria-selected", "false");
    expect(labelsRow).toHaveAttribute("aria-selected", "true");

    fireEvent.click(screen.getByRole("button", { name: "Edit labels for KNT-2" }));
    expect(detailRow).toHaveAttribute("aria-selected", "true");
    expect(labelsRow).toHaveAttribute("aria-selected", "false");
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
  return render(surface(memory, sidebarMode));
}

function surface(memory = createProjectTasksViewMemory(), sidebarMode: "overlay" | "shift" = "shift") {
  return (
    <I18nextProvider i18n={appI18n}>
      <ProjectTasksSurface projectID="project-1" sidebarMode={sidebarMode} viewMemory={memory} />
    </I18nextProvider>
  );
}

function countsQuery(counts: ProjectTaskGroupCounts["counts"]) {
  return {
    data: { projectID: "project-1", counts, generatedAt: 1 },
    error: null,
    isError: false,
    isFetching: false,
    isPending: false,
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
    pageParams: [0],
    pages: [],
    refetch: vi.fn(),
    tasks,
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

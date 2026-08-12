import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";

import type { ProjectTaskGroupCounts, TaskListItem } from "@/api";
import type * as ProjectTaskListData from "./projectTaskListData";
import { appI18n, initializeI18n } from "@/i18n";
import { createProjectTasksViewMemory } from "./projectTasksViewMemory";

const fixture = vi.hoisted(() => ({
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
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => fixture.board,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useAppNavigation: () => ({ openProject: vi.fn() }),
  useAppServices: () => ({ api: {} }),
  useConnectionSnapshot: () => ({ generation: 1, phase: "disconnected" }),
  useOwnedSidebarRoots: () => ({ open: vi.fn() }),
}));

vi.mock("./projectTaskListData", async (importOriginal) => {
  const actual = await importOriginal<typeof ProjectTaskListData>();
  return {
    ...actual,
    useProjectTaskListEvents: () => undefined,
    useProjectTaskListData: () => ({
      counts: countsQuery(fixture.counts),
      active: groupData([task("active-1", "KNT-1", "Active task"), task("active-2", "KNT-2", "Running task")]),
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
  });

  it("renders the workflow strip and the ordered grouped Task island with default disclosure", () => {
    renderSurface();

    expect(screen.getByRole("button", { name: "Delivery" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Support" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link workflow" })).toBeInTheDocument();
    expect(screen.getByRole("grid", { name: "Project tasks" })).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "Status",
      "Dependencies",
      "ID",
      "Title",
      "Workflow",
      "Labels",
    ]);
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
});

function renderSurface(memory = createProjectTasksViewMemory()) {
  return render(surface(memory));
}

function surface(memory = createProjectTasksViewMemory()) {
  return (
    <I18nextProvider i18n={appI18n}>
      <ProjectTasksSurface projectID="project-1" sidebarMode="shift" viewMemory={memory} />
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

function task(id: string, shortID: string, title: string): TaskListItem {
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

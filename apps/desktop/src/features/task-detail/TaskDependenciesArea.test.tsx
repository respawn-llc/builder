import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type { TaskDependencies } from "@/api";
import type { TaskSearchResult } from "@/app-facade";
import { TaskDependenciesArea } from "./TaskDependenciesAreaAdapter";

const searchFixture = vi.hoisted<{ results: readonly TaskSearchResult[] }>(() => ({ results: [] }));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  taskSearchDebounceMs: 0,
  useDebouncedText: (value: string) => value,
  useTaskSearch: () => ({
    displayedQuery: null,
    normalizedTooShort: false,
    paginationUsesVisibleData: true,
    request: {
      data: undefined,
      error: null,
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isError: false,
      isFetchNextPageError: false,
      isFetching: false,
      isFetchingNextPage: false,
      refetch: vi.fn(),
    },
    results: searchFixture.results,
    searchable: searchFixture.results.length > 0,
  }),
}));

const dependencies: TaskDependencies = {
  blockerCount: 1,
  unsatisfiedBlockerCount: 1,
  directlyBlockedTaskCount: 1,
  directions: [
    {
      direction: "blocked-by",
      totalCount: 1,
      unsatisfiedCount: 1,
      addAvailability: { kind: "available", remainingCapacity: 4 },
      items: [
        {
          taskID: "task-2",
          shortID: "KENT-2",
          title: "Prepare release",
          workflowID: "workflow-2",
          satisfaction: "unsatisfied",
          status: {
            kind: "backlog",
            nativeState: "active",
            nodeIDs: [],
            attentionTypes: [],
          },
        },
      ],
    },
    {
      direction: "blocks",
      totalCount: 1,
      unsatisfiedCount: null,
      addAvailability: { kind: "available", remainingCapacity: 3 },
      items: [
        {
          taskID: "task-3",
          shortID: "KENT-3",
          title: "Publish release",
          workflowID: "workflow-3",
          satisfaction: null,
          status: {
            kind: "running",
            nativeState: "running",
            nodeIDs: ["node-1"],
            attentionTypes: [],
          },
        },
      ],
    },
  ],
};

describe("TaskDependenciesArea", () => {
  beforeEach(() => {
    searchFixture.results = [];
  });

  it("keeps both empty directions actionable without progress", () => {
    render(
      <TaskDependenciesArea
        dependencies={{
          blockerCount: 0,
          unsatisfiedBlockerCount: 0,
          directlyBlockedTaskCount: 0,
          directions: dependencies.directions.map((direction) => ({
            ...direction,
            totalCount: 0,
            unsatisfiedCount: direction.direction === "blocked-by" ? 0 : null,
            items: [],
          })),
        }}
        disabled={false}
        navigationDisabled={false}
        onAdd={vi.fn()}
        onAddExisting={vi.fn().mockResolvedValue(undefined)}
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    expect(screen.getAllByRole("group")).toHaveLength(2);
    expect(screen.getAllByTestId(/^dependency-add-/)).toHaveLength(2);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("uses server-owned limit availability to disable Add", () => {
    render(
      <TaskDependenciesArea
        dependencies={{
          ...dependencies,
          directions: dependencies.directions.map((direction) =>
            direction.direction === "blocked-by"
              ? {
                  ...direction,
                  addAvailability: { kind: "limit_reached" as const },
                }
              : direction,
          ),
        }}
        disabled={false}
        navigationDisabled={false}
        onAdd={vi.fn()}
        onAddExisting={vi.fn().mockResolvedValue(undefined)}
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    const add = screen.getByTestId("dependency-add-blocked-by");
    expect(add).toBeDisabled();
    expect(add).toHaveAttribute("aria-describedby");
  });

  it("disables every mutation while the surface is disconnected", () => {
    render(
      <TaskDependenciesArea
        dependencies={dependencies}
        disabled
        navigationDisabled
        onAdd={vi.fn()}
        onAddExisting={vi.fn().mockResolvedValue(undefined)}
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    expect(screen.getByTestId("dependency-add-blocked-by")).toBeDisabled();
    expect(screen.getByTestId("dependency-remove-task-2")).toBeDisabled();
  });

  it("renders both typed directions and delegates relationship actions", async () => {
    const onAdd = vi.fn();
    const onRemove = vi.fn();
    const onSelectTask = vi.fn();
    const user = userEvent.setup();

    render(
      <TaskDependenciesArea
        dependencies={dependencies}
        disabled={false}
        navigationDisabled={false}
        onAdd={onAdd}
        onAddExisting={vi.fn().mockResolvedValue(undefined)}
        onRemove={onRemove}
        onSelectTask={onSelectTask}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    expect(screen.getAllByRole("group")).toHaveLength(2);
    expect(screen.getByText("KENT-2")).toBeInTheDocument();
    expect(screen.getByText("Prepare release")).toBeInTheDocument();
    const progress = screen.getByRole("progressbar");
    expect(progress).toHaveAttribute("aria-valuenow", "0");
    expect(progress).toHaveAttribute("aria-valuemax", "1");
    expect(
      screen.queryAllByRole("button").some((button) => within(button).queryByRole("progressbar") !== null),
    ).toBe(false);
    const chip = screen.getByLabelText(progress.getAttribute("aria-label") ?? "", {
      selector: "span[tabindex]",
    });
    await user.tab();
    expect(chip).toHaveFocus();
    expect(await screen.findByRole("tooltip")).not.toBeEmptyDOMElement();

    await user.click(screen.getByTestId("dependency-row-task-2"));
    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    await user.click(screen.getByRole("button", { name: "task.dependenciesCreateTask" }));
    await user.click(screen.getByTestId("dependency-remove-task-2"));

    expect(onSelectTask).toHaveBeenCalledWith("task-2");
    expect(onAdd).toHaveBeenCalledWith("blocked-by");
    expect(onRemove).toHaveBeenCalledWith({
      blockerTaskID: "task-2",
      blockedTaskID: "task-1",
    });
  });

  it("keeps the picker open across sequential selections and resets accepted IDs after close", async () => {
    const onAddExisting = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    searchFixture.results = [candidate("task-9"), candidate("task-10")];

    render(
      <TaskDependenciesArea
        dependencies={{
          blockerCount: 0,
          unsatisfiedBlockerCount: 0,
          directlyBlockedTaskCount: 0,
          directions: dependencies.directions.map((direction) => ({
            ...direction,
            totalCount: 0,
            unsatisfiedCount: direction.direction === "blocked-by" ? 0 : null,
            items: [],
          })),
        }}
        disabled={false}
        navigationDisabled={false}
        onAdd={vi.fn()}
        onAddExisting={onAddExisting}
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    await user.click(screen.getByTestId("dependency-candidate-task-9"));
    expect(screen.queryByTestId("dependency-candidate-task-9")).not.toBeInTheDocument();
    expect(screen.getByTestId("dependency-candidate-task-10")).toBeInTheDocument();
    await user.click(screen.getByTestId("dependency-candidate-task-10"));

    expect(onAddExisting).toHaveBeenNthCalledWith(1, {
      blockerTaskID: "task-9",
      blockedTaskID: "task-1",
    });
    expect(onAddExisting).toHaveBeenNthCalledWith(2, {
      blockerTaskID: "task-10",
      blockedTaskID: "task-1",
    });

    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    expect(screen.getByTestId("dependency-candidate-task-9")).toBeInTheDocument();
  });

  it("unmounts a populated picker when Task Detail reaches its direction limit", async () => {
    const user = userEvent.setup();
    searchFixture.results = [candidate("task-9")];
    const props = {
      disabled: false,
      navigationDisabled: false,
      onAdd: vi.fn(),
      onAddExisting: vi.fn().mockResolvedValue(undefined),
      onRemove: vi.fn(),
      onSelectTask: vi.fn(),
      projectID: "project-1",
      taskID: "task-1",
    } as const;
    const oneRemaining: TaskDependencies = {
      ...dependencies,
      directions: dependencies.directions.map((direction) =>
        direction.direction === "blocked-by"
          ? { ...direction, addAvailability: { kind: "available" as const, remainingCapacity: 1 } }
          : direction,
      ),
    };
    const { rerender } = render(<TaskDependenciesArea dependencies={oneRemaining} {...props} />);

    await user.click(screen.getByTestId("dependency-add-blocked-by"));
    expect(screen.getByTestId("dependency-candidate-task-9")).toBeInTheDocument();

    rerender(
      <TaskDependenciesArea
        dependencies={{
          ...oneRemaining,
          directions: oneRemaining.directions.map((direction) =>
            direction.direction === "blocked-by"
              ? { ...direction, addAvailability: { kind: "limit_reached" as const } }
              : direction,
          ),
        }}
        {...props}
      />,
    );

    expect(screen.queryByTestId("dependency-candidate-task-9")).not.toBeInTheDocument();
    const add = screen.getByTestId("dependency-add-blocked-by");
    expect(add).toBeDisabled();
    expect(add).toHaveAttribute("aria-describedby");
  });

  it("blocks relationship navigation while keeping Remove independently available", async () => {
    const onAdd = vi.fn();
    const onRemove = vi.fn();
    const onSelectTask = vi.fn();
    const user = userEvent.setup();

    render(
      <TaskDependenciesArea
        dependencies={dependencies}
        disabled={false}
        navigationDisabled
        onAdd={onAdd}
        onAddExisting={vi.fn().mockResolvedValue(undefined)}
        onRemove={onRemove}
        onSelectTask={onSelectTask}
        projectID="project-1"
        taskID="task-1"
      />,
    );

    expect(screen.getByTestId("dependency-add-blocked-by")).toBeDisabled();
    const dependencyButton = screen
      .getAllByRole("button")
      .find((button) => within(button).queryByTestId("dependency-row-task-2") !== null);
    if (dependencyButton === undefined) throw new Error("Expected the dependency row button.");
    expect(dependencyButton).toBeDisabled();
    expect(screen.getByTestId("dependency-remove-task-2")).toBeEnabled();

    await user.click(screen.getByTestId("dependency-remove-task-2"));
    expect(onRemove).toHaveBeenCalledOnce();
    expect(onAdd).not.toHaveBeenCalled();
    expect(onSelectTask).not.toHaveBeenCalled();
  });
});

function candidate(taskID: string): TaskSearchResult {
  return {
    key: taskID,
    group: {
      projectID: "project-1",
      projectKey: "KENT",
      taskID,
      shortID: taskID,
      workflowID: "workflow-1",
      title: taskID,
      status: { kind: "backlog", nativeState: "active", nodeIDs: [], attentionTypes: [] },
      totalHitCount: 1,
      hits: [{
        ordinal: 1,
        source: { kind: "title" },
        literal: { before: "", match: taskID, after: "", leftTruncated: false, rightTruncated: false },
      }],
    },
  };
}

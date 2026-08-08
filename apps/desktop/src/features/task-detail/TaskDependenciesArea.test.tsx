import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type { TaskDependencies } from "@/api";
import { TaskDependenciesArea } from "./TaskDependenciesArea";

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
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
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
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
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
        onRemove={vi.fn()}
        onSelectTask={vi.fn()}
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
        onRemove={onRemove}
        onSelectTask={onSelectTask}
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
    await user.click(screen.getByTestId("dependency-remove-task-2"));

    expect(onSelectTask).toHaveBeenCalledWith("task-2");
    expect(onAdd).toHaveBeenCalledWith("blocked-by");
    expect(onRemove).toHaveBeenCalledWith({
      blockerTaskID: "task-2",
      blockedTaskID: "task-1",
    });
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
        onRemove={onRemove}
        onSelectTask={onSelectTask}
        taskID="task-1"
      />,
    );

    expect(screen.getByTestId("dependency-add-blocked-by")).toBeDisabled();
    const dependencyButton = screen.getAllByRole("button").find(
      (button) => within(button).queryByTestId("dependency-row-task-2") !== null,
    );
    if (dependencyButton === undefined) throw new Error("Expected the dependency row button.");
    expect(dependencyButton).toBeDisabled();
    expect(screen.getByTestId("dependency-remove-task-2")).toBeEnabled();

    await user.click(screen.getByTestId("dependency-remove-task-2"));
    expect(onRemove).toHaveBeenCalledOnce();
    expect(onAdd).not.toHaveBeenCalled();
    expect(onSelectTask).not.toHaveBeenCalled();
  });
});

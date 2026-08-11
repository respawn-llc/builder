import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { TaskLabelFilter } from "@/api";
import { BoardQueryProvider } from "./BoardQueryContext";
import { useBoardQuery } from "./BoardQueryRuntime";

const labelFilter: TaskLabelFilter = {
  kind: "named",
  mode: "all",
  labelIDs: ["label-1"],
};

describe("board filter route lifecycle", () => {
  it("resets Unblocked when Project or Workflow changes while retaining Labels", async () => {
    const user = userEvent.setup();
    const view = render(<TestApp projectID="project-1" workflowID="workflow-1" />);

    await user.click(screen.getByRole("button", { name: "select unblocked" }));
    expect(screen.getByTestId("dependency-filter")).toHaveTextContent("true");
    expect(screen.getByTestId("label-filter")).toHaveTextContent("named");

    view.rerender(<TestApp projectID="project-2" workflowID="workflow-2" />);
    expect(screen.getByTestId("dependency-filter")).toHaveTextContent("null");
    expect(screen.getByTestId("label-filter")).toHaveTextContent("named");
  });

  it("resets Unblocked after leaving and returning to the same board", async () => {
    const user = userEvent.setup();
    const view = render(<TestApp projectID="project-1" workflowID="workflow-1" />);

    await user.click(screen.getByRole("button", { name: "select unblocked" }));
    expect(screen.getByTestId("dependency-filter")).toHaveTextContent("true");

    view.rerender(<TestApp projectID="project-1" workflowID="workflow-1" surface="non-board" />);
    view.rerender(<TestApp projectID="project-1" workflowID="workflow-1" />);

    expect(screen.getByTestId("dependency-filter")).toHaveTextContent("null");
    expect(screen.getByTestId("label-filter")).toHaveTextContent("named");
  });

  it("resets the route-local board sort when the board key changes", async () => {
    const user = userEvent.setup();
    const view = render(<TestApp projectID="project-1" workflowID="workflow-1" />);

    await user.click(screen.getByRole("button", { name: "select created sort" }));
    expect(screen.getByTestId("board-sort")).toHaveTextContent("created:asc");

    view.rerender(<TestApp projectID="project-2" workflowID="workflow-2" />);
    expect(screen.getByTestId("board-sort")).toHaveTextContent("updated:desc");
  });
});

function TestApp({
  projectID,
  surface = "board",
  workflowID,
}: Readonly<{
  projectID: string;
  surface?: "board" | "non-board";
  workflowID: string;
}>) {
  return surface === "board" ? (
    <BoardQueryProvider key={`${projectID}:${workflowID}`} labelFilter={labelFilter}>
      <FilterProbe />
    </BoardQueryProvider>
  ) : (
    <div data-testid="non-board">Inbox</div>
  );
}

function FilterProbe() {
  const runtime = useBoardQuery();
  return (
    <>
      <output data-testid="dependency-filter">{String(runtime.filter.dependencyFilter)}</output>
      <output data-testid="label-filter">{runtime.filter.labelFilter.kind}</output>
      <output data-testid="board-sort">
        {runtime.sort.field}:{runtime.sort.direction}
      </output>
      <button
        onClick={() => {
          runtime.setDependencyFilter(true);
        }}
        type="button"
      >
        select unblocked
      </button>
      <button
        onClick={() => {
          runtime.setSort({ field: "created", direction: "asc" });
        }}
        type="button"
      >
        select created sort
      </button>
    </>
  );
}

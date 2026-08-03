import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useMemo } from "react";

import { canonicalBoardFilter, type TaskLabelFilter } from "@/api";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { BoardFilterGenerationProvider } from "./BoardFilterGenerationContext";

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
  const queryClient = useMemo(() => new QueryClient(), []);
  return (
    <QueryClientProvider client={queryClient}>
      {surface === "board" ? (
        <BoardFilterGenerationProvider
          desiredLabelFilter={labelFilter}
          initialFilter={canonicalBoardFilter({ labelFilter, dependencyFilter: null })}
          key={`${projectID}:${workflowID}`}
        >
          <FilterProbe />
        </BoardFilterGenerationProvider>
      ) : (
        <div data-testid="non-board">Inbox</div>
      )}
    </QueryClientProvider>
  );
}

function FilterProbe() {
  const runtime = useBoardFilterGeneration();
  const { active } = runtime.snapshot;
  return (
    <>
      <output data-testid="dependency-filter">{String(active.filter.dependencyFilter)}</output>
      <output data-testid="label-filter">{active.filter.labelFilter.kind}</output>
      <button
        onClick={() => {
          runtime.controller.setDesiredFilter({
            labelFilter: active.filter.labelFilter,
            dependencyFilter: true,
          });
        }}
        type="button"
      >
        select unblocked
      </button>
    </>
  );
}

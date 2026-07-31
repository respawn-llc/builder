import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type { TaskStartResponse, WorkflowExecutionTargetSelection } from "@/api";
import {
  startTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "./index";
import type { TaskInitiatingActionResult } from "./executionTargetContinuation";

type ExecuteStub = (
  action: TaskInitiatingAction,
  selection?: WorkflowExecutionTargetSelection,
) => Promise<Readonly<{ kind: "start"; response: TaskStartResponse }>>;

describe("TaskInitiatingActionDialogs", () => {
  it("shows dependency confirmation before target selection and preserves proceed intent", async () => {
    const execute = vi
      .fn<ExecuteStub>()
      .mockResolvedValueOnce({
        kind: "start",
        response: {
          outcome: "dependency_confirmation_required",
          unsatisfiedDependencyCount: 2,
        },
      })
      .mockResolvedValueOnce({
        kind: "start",
        response: {
          outcome: "selection_required",
          selectionRequired: { reason: "policy_requires_selection" },
        },
      })
      .mockResolvedValueOnce({
        kind: "start",
        response: {
          outcome: "applied",
          applied: {
            currentNodes: [
              {
                nodeID: "node-1",
                transitionBranchKey: null,
                sessionID: null,
              },
            ],
          },
        },
      });
    const onApplied = vi.fn<(result: TaskInitiatingActionResult) => void>();
    render(<Harness execute={execute} onApplied={onApplied} onViewDependencies={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-proceed"));
    expect(await screen.findByRole("radiogroup")).toBeInTheDocument();
    expect(execute.mock.calls[1]?.[0]).toMatchObject({
      proceedDespiteDependencies: true,
    });

    await user.click(screen.getByTestId("execution-target-submit"));
    await waitFor(() => {
      expect(onApplied).toHaveBeenCalledOnce();
    });
    expect(execute.mock.calls[2]?.[0]).toMatchObject({
      proceedDespiteDependencies: true,
    });
  });

  it("abandons the initiating action when View dependencies is selected", async () => {
    const onViewDependencies = vi.fn<(taskID: string) => void>();
    render(
      <Harness
        execute={vi.fn<ExecuteStub>().mockResolvedValue({
          kind: "start",
          response: {
            outcome: "dependency_confirmation_required",
            unsatisfiedDependencyCount: 1,
          },
        })}
        onApplied={vi.fn<(result: TaskInitiatingActionResult) => void>()}
        onViewDependencies={onViewDependencies}
      />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-view"));

    expect(onViewDependencies).toHaveBeenCalledWith("task-1");
    expect(screen.queryByTestId("dependency-confirmation-view")).not.toBeInTheDocument();
  });
});

function Harness({
  execute,
  onApplied,
  onViewDependencies,
}: Readonly<{
  execute: ExecuteStub;
  onApplied: (result: TaskInitiatingActionResult) => void;
  onViewDependencies: (taskID: string) => void;
}>) {
  const controller = useTaskInitiatingActionController({
    execute: async (action, selection) => {
      const result = await execute(action, selection);
      if (action.kind !== "start") {
        throw new Error("Dialog test only supports Start actions.");
      }
      return { kind: result.kind, response: result.response, action };
    },
    onApplied,
    onAppliedError: vi.fn(),
  });
  return (
    <>
      <button
        data-testid="initiate-action"
        onClick={() => {
          void controller.run(startTaskInitiatingAction("task-1"));
        }}
        type="button"
      />
      <TaskInitiatingActionDialogs continuation={controller} onViewDependencies={onViewDependencies} />
    </>
  );
}

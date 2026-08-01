import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type { TaskStartResponse, WorkflowExecutionTargetSelection } from "@/api";
import {
  startTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  type TaskInitiatingActionDialogResult,
  useTaskInitiatingActionController,
} from "./index";
import type { TaskInitiatingActionResult } from "./executionTargetContinuation";

type ExecuteStub = (
  action: TaskInitiatingAction,
  selection?: WorkflowExecutionTargetSelection,
) => Promise<Readonly<{ kind: "start"; response: TaskStartResponse }>>;

describe("TaskInitiatingActionDialogs", () => {
  it("closes dependency confirmation and returns approval without executing it", async () => {
    const execute = vi.fn<ExecuteStub>().mockResolvedValue({
      kind: "start",
      response: {
        outcome: "dependency_confirmation_required",
        unsatisfiedDependencyCount: 2,
      },
    });
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    render(<Harness execute={execute} onResult={onResult} />);
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-proceed"));
    const result = onResult.mock.calls[0]?.[0];
    expect(result?.kind).toBe("continue");
    if (result?.kind !== "continue") {
      throw new Error("Expected a continue result.");
    }
    expect(result.action.proceedDespiteDependencies).toBe(true);
    expect(result.selection).toBeUndefined();
    expect(execute).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("dependency-confirmation-proceed")).not.toBeInTheDocument();
  });

  it("closes dependency confirmation and returns View dependencies", async () => {
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    render(
      <Harness
        execute={vi.fn<ExecuteStub>().mockResolvedValue({
          kind: "start",
          response: {
            outcome: "dependency_confirmation_required",
            unsatisfiedDependencyCount: 1,
          },
        })}
        onResult={onResult}
      />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-view"));

    expect(onResult).toHaveBeenCalledWith({ kind: "view_dependencies", taskID: "task-1" });
    expect(screen.queryByTestId("dependency-confirmation-view")).not.toBeInTheDocument();
  });

  it("closes target selection and returns the selected target without executing it", async () => {
    const execute = vi.fn<ExecuteStub>().mockResolvedValue({
      kind: "start",
      response: {
        outcome: "selection_required",
        selectionRequired: { reason: "policy_requires_selection" },
      },
    });
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    render(<Harness execute={execute} onResult={onResult} />);
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("execution-target-submit"));

    const result = onResult.mock.calls[0]?.[0];
    expect(result?.kind).toBe("continue");
    if (result?.kind !== "continue") {
      throw new Error("Expected a continue result.");
    }
    expect(result.action.proceedDespiteDependencies).toBe(false);
    expect(result.selection).toEqual({ mode: "default_branch", customRef: null });
    expect(execute).toHaveBeenCalledOnce();
    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
  });
});

function Harness({
  execute,
  onResult,
}: Readonly<{
  execute: ExecuteStub;
  onResult: (result: TaskInitiatingActionDialogResult) => void;
}>) {
  const controller = useTaskInitiatingActionController({
    execute: async (action, selection) => {
      const result = await execute(action, selection);
      if (action.kind !== "start") {
        throw new Error("Dialog test only supports Start actions.");
      }
      return { kind: result.kind, response: result.response, action };
    },
    onApplied: vi.fn<(result: TaskInitiatingActionResult) => void>(),
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
      <TaskInitiatingActionDialogs continuation={controller} onResult={onResult} />
    </>
  );
}

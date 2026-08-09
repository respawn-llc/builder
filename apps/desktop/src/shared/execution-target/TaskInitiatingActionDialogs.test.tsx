import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { RpcError, type TaskStartResponse, type WorkflowExecutionTargetSelection } from "@/api";
import { registeredWorktreeWire } from "@/test-support/api";
import {
  TestAppProviders,
  createTestServices,
  type TestAppServices,
} from "@/test-support/app-services";
import {
  executeTaskInitiatingAction,
  moveTaskInitiatingAction,
  startTaskInitiatingAction,
  TaskInitiatingActionDialogs,
  type TaskInitiatingAction,
  type TaskInitiatingActionController,
  type TaskInitiatingActionDialogResult,
  useTaskInitiatingActionController,
} from "./index";
import type { TaskInitiatingActionResult } from "./executionTargetContinuation";

type ExecuteStub = (
  action: TaskInitiatingAction,
  selection?: WorkflowExecutionTargetSelection,
) => Promise<Readonly<{ kind: "start"; response: TaskStartResponse }>>;

const appServices = createTestServices([], undefined, { platform: "macos" });

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
    render(
      <TestAppProviders services={appServices}>
        <Harness execute={execute} onResult={onResult} />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-proceed"));
    const result = onResult.mock.calls[0]?.[0];
    expect(result?.kind).toBe("continue");
    if (result?.kind !== "continue") {
      throw new Error("Expected a continue result.");
    }
    if (result.action.kind !== "start") {
      throw new Error("Expected a Start action.");
    }
    expect(result.action.proceedDespiteDependencies).toBe(true);
    expect(result.selection).toBeUndefined();
    expect(execute).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("dependency-confirmation-proceed")).not.toBeInTheDocument();
  });

  it("closes dependency confirmation and returns View dependencies", async () => {
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    render(
      <TestAppProviders services={appServices}>
        <Harness
          execute={vi.fn<ExecuteStub>().mockResolvedValue({
            kind: "start",
            response: {
              outcome: "dependency_confirmation_required",
              unsatisfiedDependencyCount: 1,
            },
          })}
          onResult={onResult}
        />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("dependency-confirmation-view"));

    expect(onResult).toHaveBeenCalledWith({ kind: "view_dependencies", taskID: "task-1" });
    expect(screen.queryByTestId("dependency-confirmation-view")).not.toBeInTheDocument();
  });

  it("keeps target selection while the selected target action is submitted", async () => {
    const execute = vi.fn<ExecuteStub>().mockResolvedValue({
      kind: "start",
      response: {
        outcome: "selection_required",
        selectionRequired: { reason: "policy_requires_selection" },
      },
    });
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    render(
      <TestAppProviders services={appServices}>
        <Harness execute={execute} onResult={onResult} />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-action"));
    await user.click(await screen.findByTestId("execution-target-submit"));

    const result = onResult.mock.calls[0]?.[0];
    expect(result?.kind).toBe("continue");
    if (result?.kind !== "continue") {
      throw new Error("Expected a continue result.");
    }
    if (result.action.kind !== "start") {
      throw new Error("Expected a Start action.");
    }
    expect(result.action.proceedDespiteDependencies).toBe(false);
    expect(result.selection).toEqual({ mode: "default_branch", customRef: null });
    expect(execute).toHaveBeenCalledOnce();
    expect(screen.getByRole("radiogroup")).toBeInTheDocument();
  });

  it("presents typed Move setup recovery and preserves retry target intent", async () => {
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: "ship",
      values: { agent: { result: "ready" } },
      commentary: "operator override",
      proceedDespiteDependencies: true,
    });
    const selection = { mode: "custom_ref", customRef: "feature/original" } as const;
    const onResult = vi.fn<(result: TaskInitiatingActionDialogResult) => void>();
    const continuation: TaskInitiatingActionController = {
      pending: {
        kind: "setup_recovery",
        action,
        failure: {
          kind: "setup_script",
          diagnostic: "setup failed after retry",
          scriptPath: "/repo/setup.sh",
          retainedWorktree: { root: "/repo/current" },
          retainedPreviousWorktree: { root: "/repo/previous" },
        },
        targetIntent: { kind: "explicit_override", selection },
        selection: null,
      },
      running: false,
      run: vi.fn(),
      openSetupRecovery: vi.fn(),
      chooseAnotherTarget: vi.fn(),
      close: vi.fn(),
      selectMode: vi.fn(),
      setCustomRef: vi.fn(),
    };
    render(
      <TestAppProviders services={appServices}>
        <TaskInitiatingActionDialogs continuation={continuation} onResult={onResult} />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    expect(screen.getByText("setup failed after retry")).toBeInTheDocument();
    expect(screen.getByText("/repo/setup.sh")).toBeInTheDocument();
    expect(screen.getByText("/repo/current")).toBeInTheDocument();
    expect(screen.getByText("/repo/previous")).toBeInTheDocument();
    await user.click(screen.getByTestId("setup-recovery-retry"));
    expect(onResult).toHaveBeenCalledWith({
      kind: "continue",
      action,
      selection,
    });
    await user.click(screen.getByTestId("setup-recovery-choose"));
    expect(continuation.chooseAnotherTarget).toHaveBeenCalledOnce();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(continuation.close).toHaveBeenCalledOnce();
  });

  it("runs fixed-policy Move recovery through the RPC boundary without setup observation", async () => {
    const retry = deferred<unknown>();
    const services = createTestServices([
      {
        method: "workflow.task.move",
        async handler(_params, callIndex) {
          if (callIndex === 0) {
            throw setupFailure("fixed-policy failure", "/repo/current", "/repo/previous");
          }
          return retry.promise;
        },
      },
    ]);
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: "ship",
      values: { agent: { result: "ready" } },
      commentary: "operator override",
      proceedDespiteDependencies: true,
    });
    render(
      <TestAppProviders services={services}>
        <MoveRecoveryHarness action={action} services={services} />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-move"));
    expect(await screen.findByText("fixed-policy failure")).toBeInTheDocument();
    expect(screen.getByText("/repo/current")).toBeInTheDocument();
    expect(screen.getByText("/repo/previous")).toBeInTheDocument();

    await user.click(screen.getByTestId("setup-recovery-retry"));
    await user.click(screen.getByTestId("setup-recovery-retry"));
    expect(moveCalls(services)).toHaveLength(2);
    expect(moveCalls(services)[0]?.params).toEqual(moveCalls(services)[1]?.params);
    expect(moveCalls(services)[0]?.params).toMatchObject({
      task_id: "task-1",
      target_node_id: "node-2",
      transition_key: "ship",
      values: { agent: { result: "ready" } },
      commentary: "operator override",
      proceed_despite_dependencies: true,
    });
    expect(moveCalls(services)[0]?.params).not.toHaveProperty("setup_operation_id");

    act(() => {
      retry.resolve({
        outcome: "applied",
        applied: {
          current_nodes: [{ node_id: "node-2", transition_branch_key: null, session_id: null }],
          retained_previous_worktree: {
            worktree: registeredWorktreeWire("/repo/previous", "worktree-previous"),
          },
        },
      });
    });
    await waitFor(() => {
      expect(screen.queryByTestId("setup-recovery-retry")).not.toBeInTheDocument();
    });
    expect(screen.queryByText("/repo/previous")).not.toBeInTheDocument();
    expect(
      services.transport.subscriptionStarts.some(
        (subscription) => subscription.method === "worktree.setup.subscribe",
      ),
    ).toBe(false);
  });

  it("preserves a selection-required Move through retry and target replacement", async () => {
    const services = createTestServices([
      {
        method: "workflow.task.move",
        handler(_params, callIndex) {
          switch (callIndex) {
            case 0:
              return {
                outcome: "selection_required",
                selection_required: { reason: "policy_requires_selection" },
              };
            case 1:
              throw setupFailure("selected target failed", "/repo/current");
            case 2:
              throw setupFailure("selected target failed again", "/repo/current");
            default:
              return {
                outcome: "no_op",
                no_op: {
                  current_nodes: [{ node_id: "node-2", transition_branch_key: null, session_id: null }],
                },
              };
          }
        },
      },
    ]);
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: "ship",
      values: { agent: { result: "ready" } },
      commentary: "preserve selection",
      proceedDespiteDependencies: true,
    });
    render(
      <TestAppProviders services={services}>
        <MoveRecoveryHarness action={action} services={services} />
      </TestAppProviders>,
    );
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-move"));
    await user.click(await screen.findByTestId("execution-target-submit"));
    expect(await screen.findByText("selected target failed")).toBeInTheDocument();
    await user.click(screen.getByTestId("setup-recovery-retry"));
    expect(await screen.findByText("selected target failed again")).toBeInTheDocument();
    await user.click(screen.getByTestId("setup-recovery-choose"));
    await user.click(screen.getByRole("radio", { name: /Source workspace/i }));
    await user.click(screen.getByTestId("setup-recovery-target-submit"));
    await waitFor(() => {
      expect(screen.queryByTestId("setup-recovery-target-submit")).not.toBeInTheDocument();
    });

    const calls = moveCalls(services);
    expect(calls).toHaveLength(4);
    expect(calls[1]?.params).toMatchObject({
      task_id: "task-1",
      target_node_id: "node-2",
      transition_key: "ship",
      values: { agent: { result: "ready" } },
      commentary: "preserve selection",
      proceed_despite_dependencies: true,
      execution_target: { mode: "default_branch" },
    });
    expect(calls[2]?.params).toEqual(calls[1]?.params);
    expect(calls[3]?.params).toMatchObject({
      task_id: "task-1",
      target_node_id: "node-2",
      transition_key: "ship",
      values: { agent: { result: "ready" } },
      commentary: "preserve selection",
      proceed_despite_dependencies: true,
      execution_target: { mode: "none" },
    });
    expect(
      services.transport.subscriptionStarts.some(
        (subscription) => subscription.method === "worktree.setup.subscribe",
      ),
    ).toBe(false);
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

function MoveRecoveryHarness({
  action,
  services,
}: Readonly<{
  action: Extract<TaskInitiatingAction, { kind: "move" }>;
  services: TestAppServices;
}>) {
  const controller = useTaskInitiatingActionController({
    execute: async (submittedAction, selection) =>
      executeTaskInitiatingAction(services.api, submittedAction, selection),
    onApplied: vi.fn(),
    onAppliedError: vi.fn(),
  });
  return (
    <>
      <button
        data-testid="initiate-move"
        onClick={() => {
          void controller.run(action);
        }}
        type="button"
      />
      <TaskInitiatingActionDialogs
        continuation={controller}
        onResult={(result) => {
          if (result.kind === "continue") {
            void controller.run(result.action, result.selection);
          }
        }}
      />
    </>
  );
}

function moveCalls(services: TestAppServices) {
  return services.transport.calls.filter((call) => call.method === "workflow.task.move");
}

function setupFailure(diagnostic: string, root: string, previousRoot?: string): RpcError {
  return new RpcError({
    code: -32061,
    method: "workflow.task.move",
    message: "display-only preparation error",
    data: {
      type: "workflow_task_move_preparation",
      failure: {
        retry_readiness: "retry_ready",
        cause: { kind: "operational", operational: {} },
        diagnostic,
        script_path: "/repo/setup.sh",
        retained_worktree: registeredWorktreeWire(root, "worktree-current"),
        ...(previousRoot === undefined
          ? {}
          : {
              retained_previous_worktree: {
                worktree: registeredWorktreeWire(previousRoot, "worktree-previous"),
              },
            }),
      },
    },
  });
}

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  resolve(value: T): void;
}> {
  let resolvePromise: ((value: T) => void) | undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return {
    promise,
    resolve(value) {
      if (resolvePromise === undefined) {
        throw new Error("Deferred promise resolver is unavailable.");
      }
      resolvePromise(value);
    },
  };
}

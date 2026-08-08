import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import { RpcError, type WorkflowExecutionTargetSelection } from "@/api";
import { registeredWorktreeWire } from "@/test-support/api";
import type { TaskInitiatingActionResult } from "./executionTargetContinuation";
import {
  moveTaskInitiatingAction,
  proceedWithTaskInitiatingAction,
  startTaskInitiatingAction,
  type TaskInitiatingAction,
  useTaskInitiatingActionController,
} from "./index";

describe("task initiating action controller", () => {
  it("keeps one action identity through dependency and target continuation", async () => {
    let callCount = 0;
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "start") {
        throw new Error("Controller test only supports Start actions.");
      }
      callCount += 1;
      return callCount === 1
        ? {
            kind: "start",
            action,
            response: {
              outcome: "dependency_confirmation_required",
              unsatisfiedDependencyCount: 2,
            },
          }
        : {
            kind: "start",
            action,
            response: {
              outcome: "selection_required",
              selectionRequired: { reason: "policy_requires_selection" },
            },
          };
    });
    const onApplied = vi.fn<(result: TaskInitiatingActionResult) => void>();
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied,
        onAppliedError: vi.fn(),
      }),
    );
    const action = startTaskInitiatingAction("task-1");

    await act(async () => result.current.run(action));
    expect(result.current.pending).toMatchObject({
      kind: "dependency_confirmation",
      action: { proceedDespiteDependencies: false },
      unsatisfiedDependencyCount: 2,
    });

    const dependencyPending = result.current.pending;
    if (dependencyPending?.kind !== "dependency_confirmation") {
      throw new Error("Expected dependency confirmation.");
    }
    act(() => {
      result.current.close();
    });
    await act(async () => result.current.run(proceedWithTaskInitiatingAction(dependencyPending.action)));
    expect(result.current.pending).toMatchObject({
      kind: "execution_target",
      action: {
        proceedDespiteDependencies: true,
        setupOperationID: action.setupOperationID,
      },
    });

    act(() => {
      result.current.close();
    });
    expect(result.current.pending).toBeNull();
    expect(onApplied).not.toHaveBeenCalled();
  });

  it("prevents duplicate parent-owned submission", async () => {
    const completion = deferred<TaskInitiatingActionResult>();
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "start") {
        throw new Error("Controller test only supports Start actions.");
      }
      return completion.promise;
    });
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );
    const action = startTaskInitiatingAction("task-1");
    await act(async () => {
      const first = result.current.run(action);
      const second = result.current.run(action);
      expect(execute).toHaveBeenCalledOnce();
      completion.resolve({
        kind: "start",
        action,
        response: {
          outcome: "applied",
          applied: {
            currentNodes: [
              {
                effectiveAssignee: null,
                effectiveThinking: null,
                nodeID: "node-1",
                transitionBranchKey: null,
                sessionID: null,
              },
            ],
          },
        },
      });
      await Promise.all([first, second]);
    });
    expect(execute).toHaveBeenCalledOnce();
  });

  it("carries proceed intent through executable Move continuation", async () => {
    let callCount = 0;
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "move") {
        throw new Error("Move controller test requires a Move action.");
      }
      callCount += 1;
      return {
        kind: "move",
        action,
        response:
          callCount === 1
            ? {
                outcome: "dependency_confirmation_required",
                unsatisfiedDependencyCount: 1,
              }
            : {
                outcome: "selection_required",
                selectionRequired: { reason: "policy_requires_selection" },
              },
      };
    });
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );

    await act(async () =>
      result.current.run(
        moveTaskInitiatingAction({
          taskID: "task-1",
          targetNodeID: "node-2",
        }),
      ),
    );
    const dependencyPending = result.current.pending;
    if (dependencyPending?.kind !== "dependency_confirmation") {
      throw new Error("Expected dependency confirmation.");
    }
    act(() => {
      result.current.close();
    });
    await act(async () => result.current.run(proceedWithTaskInitiatingAction(dependencyPending.action)));

    expect(result.current.pending).toMatchObject({
      kind: "execution_target",
      action: {
        kind: "move",
        input: {
          taskID: "task-1",
          targetNodeID: "node-2",
          proceedDespiteDependencies: true,
        },
      },
    });
  });

  it("preserves an existing Move proceed intent in its single input authority", () => {
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      proceedDespiteDependencies: true,
    });

    expect(action.input.proceedDespiteDependencies).toBe(true);
  });

  it("keeps the execution-target draft when the submitted action fails", async () => {
    let callCount = 0;
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "move") {
        throw new Error("Controller test requires a Move action.");
      }
      callCount += 1;
      if (callCount === 1) {
        return {
          kind: "move",
          action,
          response: {
            outcome: "selection_required",
            selectionRequired: { reason: "policy_requires_selection" },
          },
        };
      }
      throw new Error("execution target setup failed");
    });
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
    });

    await act(async () => result.current.run(action));
    act(() => {
      result.current.selectMode("custom_ref");
      result.current.setCustomRef("feature/manual-move");
    });
    const pending = result.current.pending;
    if (pending?.kind !== "execution_target") {
      throw new Error("Expected execution target selection.");
    }

    await act(async () => {
      await expect(
        result.current.run(action, {
          mode: "custom_ref",
          customRef: "feature/manual-move",
        }),
      ).rejects.toThrow("execution target setup failed");
    });

    expect(result.current.pending).toMatchObject({
      kind: "execution_target",
      action,
      selection: { mode: "custom_ref", customRef: "feature/manual-move" },
    });
  });

  it("preserves a fixed-policy Move and refreshes typed setup recovery until success", async () => {
    const retry = deferred<TaskInitiatingActionResult>();
    let callCount = 0;
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "move") {
        throw new Error("Move recovery test requires a Move action.");
      }
      callCount += 1;
      if (callCount === 1) {
        throw setupFailure("first diagnostic", "/repo/current", "/repo/previous");
      }
      return retry.promise;
    });
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: "ship",
      values: { agent: { result: "ready" } },
      commentary: "operator override",
      proceedDespiteDependencies: true,
    });

    await act(async () => {
      expect(await result.current.run(action)).toBe("setup_recovery");
    });
    expect(result.current.pending).toMatchObject({
      kind: "setup_recovery",
      action,
      targetIntent: { kind: "configured_policy" },
      failure: {
        kind: "setup_script",
        diagnostic: "first diagnostic",
        retainedWorktree: { root: "/repo/current" },
        retainedPreviousWorktree: { root: "/repo/previous" },
      },
    });

    let firstRetry: Promise<unknown> | undefined;
    let duplicateRetry: Promise<unknown> | undefined;
    act(() => {
      firstRetry = result.current.run(action);
      duplicateRetry = result.current.run(action);
    });
    expect(execute).toHaveBeenCalledTimes(2);
    expect(result.current.running).toBe(true);
    expect(result.current.pending?.kind).toBe("setup_recovery");

    retry.resolve({
      kind: "move",
      action,
      response: {
        outcome: "applied",
        applied: { currentNodes: [], retainedPreviousWorktree: null },
      },
    });
    await act(async () => {
      await Promise.all([firstRetry, duplicateRetry]);
    });
    expect(result.current.pending).toBeNull();
    expect(result.current.running).toBe(false);
  });

  it("preserves a previous Worktree when replacement target preparation fails before a primary root exists", async () => {
    const execute = vi.fn(async (): Promise<TaskInitiatingActionResult> => {
      throw targetPreparationFailure("replacement creation failed", "/repo/previous");
    });
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: undefined,
      values: {},
      commentary: "",
      proceedDespiteDependencies: false,
    });

    await act(async () => {
      expect(await result.current.run(action)).toBe("setup_recovery");
    });
    expect(result.current.pending).toMatchObject({
      kind: "setup_recovery",
      failure: {
        kind: "target_preparation",
        diagnostic: "replacement creation failed",
        scriptPath: null,
        retainedWorktree: null,
        retainedPreviousWorktree: { root: "/repo/previous" },
      },
    });
  });

  it("retains an explicit Move target through setup recovery and replaces only that intent", async () => {
    const selections: (WorkflowExecutionTargetSelection | undefined)[] = [];
    let callCount = 0;
    const execute = vi.fn(
      async (
        action: TaskInitiatingAction,
        selection?: WorkflowExecutionTargetSelection,
      ): Promise<TaskInitiatingActionResult> => {
        if (action.kind !== "move") {
          throw new Error("Move recovery test requires a Move action.");
        }
        selections.push(selection);
        callCount += 1;
        if (callCount === 1) {
          return {
            kind: "move",
            action,
            response: {
              outcome: "selection_required",
              selectionRequired: { reason: "policy_requires_selection" },
            },
          };
        }
        if (callCount < 4) {
          throw setupFailure(`diagnostic-${String(callCount)}`, "/repo/current");
        }
        return {
          kind: "move",
          action,
          response: {
            outcome: "no_op",
            noOp: { currentNodes: [] },
          },
        };
      },
    );
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
      }),
    );
    const action = moveTaskInitiatingAction({
      taskID: "task-1",
      targetNodeID: "node-2",
      transitionKey: "ship",
      values: { agent: { result: "ready" } },
      commentary: "preserve me",
      proceedDespiteDependencies: true,
    });
    const originalSelection = { mode: "custom_ref", customRef: "feature/original" } as const;

    await act(async () => result.current.run(action));
    await act(async () => {
      expect(await result.current.run(action, originalSelection)).toBe("setup_recovery");
    });
    expect(result.current.pending).toMatchObject({
      kind: "setup_recovery",
      action,
      targetIntent: { kind: "explicit_override", selection: originalSelection },
      failure: { diagnostic: "diagnostic-2" },
    });

    await act(async () => {
      expect(await result.current.run(action, originalSelection)).toBe("setup_recovery");
    });
    expect(result.current.pending).toMatchObject({
      kind: "setup_recovery",
      failure: { diagnostic: "diagnostic-3" },
    });

    const replacement = { mode: "none", customRef: null } as const;
    await act(async () => {
      expect(await result.current.run(action, replacement)).toBe("settled");
    });
    expect(selections).toEqual([undefined, originalSelection, originalSelection, replacement]);
    expect(result.current.pending).toBeNull();
  });
});

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
        retained_worktree: registeredWorktreeWire(root, "worktree-current"),
        ...(previousRoot === undefined
          ? {}
          : {
              retained_previous_worktree: {
                worktree: registeredWorktreeWire(previousRoot, "worktree-previous"),
              },
            }),
      },
      setup_script_path: "/repo/setup.sh",
    },
  });
}

function targetPreparationFailure(diagnostic: string, previousRoot: string): RpcError {
  return new RpcError({
    code: -32061,
    method: "workflow.task.move",
    message: "display-only preparation error",
    data: {
      type: "workflow_task_move_preparation",
      failure: {
        retry_readiness: "retry_ready",
        cause: { kind: "target_preparation", target_preparation: {} },
        diagnostic,
        retained_previous_worktree: {
          worktree: registeredWorktreeWire(previousRoot, "worktree-previous"),
        },
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

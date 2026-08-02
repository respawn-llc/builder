import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

import type { WorkflowExecutionTargetSelection } from "@/api";
import type { TaskInitiatingActionResult } from "./executionTargetContinuation";
import {
  moveTaskInitiatingAction,
  proceedWithTaskInitiatingAction,
  resumeTaskInitiatingAction,
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

  it("retries Resume selection with the same setup operation", async () => {
    const execute = vi
      .fn<
        (
          action: TaskInitiatingAction,
          selection?: WorkflowExecutionTargetSelection,
        ) => Promise<TaskInitiatingActionResult>
      >()
      .mockResolvedValueOnce({
        kind: "resume",
        action: resumeTaskInitiatingAction("task-1"),
        response: {
          outcome: "selection_required",
          selectionRequired: { reason: "unlocked_preparation_failed" },
        },
      })
      .mockResolvedValueOnce({
        kind: "resume",
        action: resumeTaskInitiatingAction("task-1"),
        response: {
          outcome: "applied",
          applied: { currentNodes: [{ nodeID: "node-1", transitionBranchKey: null, sessionID: null }] },
        },
      });
    const onApplied = vi.fn();
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied,
        onAppliedError: vi.fn(),
      }),
    );
    const action = resumeTaskInitiatingAction("task-1");

    await act(async () => result.current.run(action));
    expect(result.current.pending?.kind).toBe("execution_target");
    const pending = result.current.pending;
    if (pending?.kind !== "execution_target") {
      throw new Error("Expected Resume target selection.");
    }
    const selection = { mode: "none" as const, customRef: null };
    await act(async () => {
      result.current.close();
      await result.current.run(pending.action, selection);
    });
    expect(execute).toHaveBeenCalledTimes(2);
    expect(execute.mock.calls[1]?.[0]).toMatchObject({
      kind: "resume",
      taskID: "task-1",
      setupOperationID: action.setupOperationID,
    });
    expect(execute.mock.calls[1]?.[1]).toEqual(selection);
    expect(onApplied).toHaveBeenCalledOnce();
  });

  it("reports execution failures through the action error callback", async () => {
    const error = new Error("transport failed");
    const onAppliedError = vi.fn();
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute: vi.fn().mockRejectedValue(error),
        onApplied: vi.fn(),
        onAppliedError,
      }),
    );

    await act(async () => result.current.run(resumeTaskInitiatingAction("task-1")));

    expect(onAppliedError).toHaveBeenCalledWith(error);
    expect(result.current.pending).toBeNull();
    expect(result.current.running).toBe(false);
  });

  it("reports rejected Start and Move through the action-specific callback", async () => {
    const execute = vi.fn().mockRejectedValue(new Error("transport failed"));
    const onExecuteError = vi.fn();
    const { result } = renderHook(() =>
      useTaskInitiatingActionController({
        execute,
        onApplied: vi.fn(),
        onAppliedError: vi.fn(),
        onExecuteError,
      }),
    );

    const start = startTaskInitiatingAction("task-1");
    await act(async () => result.current.run(start));
    const move = moveTaskInitiatingAction({ taskID: "task-1", targetNodeID: "node-1" });
    await act(async () => result.current.run(move));

    expect(onExecuteError).toHaveBeenCalledTimes(2);
    expect(onExecuteError.mock.calls[0]?.[0]).toBe(start);
    expect(onExecuteError.mock.calls[0]?.[1]).toBeInstanceOf(Error);
    expect(onExecuteError.mock.calls[1]?.[0]).toBe(move);
    expect(onExecuteError.mock.calls[1]?.[1]).toBeInstanceOf(Error);
  });
});

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

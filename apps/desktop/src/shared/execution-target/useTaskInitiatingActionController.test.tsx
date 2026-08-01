import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

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
        proceedDespiteDependencies: true,
        input: {
          taskID: "task-1",
          targetNodeID: "node-2",
          proceedDespiteDependencies: true,
        },
      },
    });
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

import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";

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

  it("runs another action after the active action settles without a continuation", async () => {
    const first = deferred<TaskInitiatingActionResult>();
    const execute = vi.fn(async (action: TaskInitiatingAction): Promise<TaskInitiatingActionResult> => {
      if (action.kind !== "resume") {
        throw new Error("Controller test only supports Resume actions.");
      }
      if (action.taskID === "task-1") {
        return first.promise;
      }
      return {
        kind: "resume",
        action,
        response: {
          outcome: "applied",
          applied: {
            currentNodes: [
              {
                effectiveAssignee: null,
                effectiveThinking: null,
                nodeID: "node-2",
                transitionBranchKey: null,
                sessionID: null,
              },
            ],
          },
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
    const firstAction = resumeTaskInitiatingAction("task-1");
    const secondAction = resumeTaskInitiatingAction("task-2");

    await act(async () => {
      const firstRun = result.current.run(firstAction);
      const secondRun = result.current.run(secondAction);
      first.resolve({
        kind: "resume",
        action: firstAction,
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
      await Promise.all([firstRun, secondRun]);
    });

    expect(execute.mock.calls.map(([action]) => action)).toEqual([firstAction, secondAction]);
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

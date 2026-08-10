import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import {
  parseSetupOperationID,
  RpcError,
  type TaskSetupRecovery,
  type TaskStartResponse,
  type WorkflowExecutionTargetSelection,
} from "@/api";
import { TestAppProviders, createTestServices } from "@/test-support/app-services";
import {
  callParams,
  getCallCount,
  interruptedTaskAttentionResponse,
  mountTaskDetailSurface,
  taskDetailResponseWithInterruptedCurrentScript,
} from "@/test-support/task-detail";
import {
  startTaskInitiatingAction,
  moveTaskInitiatingAction,
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

const appServices = createTestServices([], undefined, { platform: "macos" });
const setupRecovery = { setupOperationID: parseSetupOperationID("55555555-5555-4555-8555-555555555555"), cause: "target_preparation",
  diagnostic: "failed", scriptPath: null, executionTarget: { mode: "head", customRef: null }, retainedWorktree: null, retainedPreviousWorktree: null } satisfies TaskSetupRecovery;

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
        <Harness execute={execute} onResult={onResult} setupRecovery={setupRecovery} />
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

  it("retries a fixed-policy Move without losing its input", async () => {
    let calls = 0;
    const execute = vi.fn(async (action: TaskInitiatingAction) => {
      calls += 1;
      if (calls === 1) throw retainedSetupError();
      return appliedMove(action);
    });
    render(<TestAppProviders services={appServices}><MoveHarness execute={execute} /></TestAppProviders>);
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-move"));
    expect(await screen.findByText("setup failed twice")).toBeInTheDocument();
    expect(screen.getByText("/worktrees/task-1")).toBeInTheDocument();
    await user.click(screen.getByTestId("setup-recovery-retry"));
    await waitFor(() => { expect(execute).toHaveBeenCalledTimes(2); });
    expect(execute.mock.calls.every(([action]) =>
      action.kind === "move" && action.input.commentary === "keep this",
    )).toBe(true);
  });

  it("replaces a post-selection Move target after actual setup failure", async () => {
    let calls = 0;
    const execute = vi.fn(async (
      action: TaskInitiatingAction,
      _selection?: WorkflowExecutionTargetSelection,
    ): Promise<TaskInitiatingActionResult> => {
      void _selection;
      calls += 1;
      if (calls === 1) return { kind: "move", action: requireMove(action), response: {
        outcome: "selection_required", selectionRequired: { reason: "policy_requires_selection" },
      } };
      if (calls === 2) throw retainedSetupError();
      return appliedMove(action);
    });
    render(<TestAppProviders services={appServices}><MoveHarness execute={execute} /></TestAppProviders>);
    const user = userEvent.setup();

    await user.click(screen.getByTestId("initiate-move"));
    await user.click(await screen.findByTestId("execution-target-submit"));
    await user.click(await screen.findByTestId("setup-recovery-choose"));
    expect(screen.getByRole("radiogroup")).toBeInTheDocument();
    await user.click(screen.getByText("Current source HEAD"));
    await user.click(screen.getByTestId("setup-recovery-target-submit"));

    await waitFor(() => { expect(execute).toHaveBeenCalledTimes(3); });
    expect(execute.mock.calls[1]?.[1]).toEqual({ mode: "default_branch", customRef: null });
    expect(execute.mock.calls[2]?.[1]).toEqual({ mode: "head", customRef: null });
    expect(execute.mock.calls[2]?.[0]).toEqual(execute.mock.calls[0]?.[0]);
    expect(screen.queryByTestId("setup-recovery-retry")).not.toBeInTheDocument();
  });

  it("recovers the canonical Task-detail interruption with its recorded target", async () => {
    const attention = { ...interruptedTaskAttentionResponse, items: [
      { ...interruptedTaskAttentionResponse.items[0], id: "attention-sibling", session_name: null, detail_json: '{"setup_recovery":{}}' },
      {
      ...interruptedTaskAttentionResponse.items[0],
      session_name: null,
      detail_json: JSON.stringify({ setup_recovery: {
        setup_operation_id: "55555555-5555-4555-8555-555555555555",
        cause: "process_exit", diagnostic: "task setup failed", script_path: "/repo/setup.sh",
        setup_requirement: "required", execution_target: { mode: "head" },
        retained_worktree: { worktree_id: "worktree-1", root: "/worktrees/task-1" },
        retained_previous_worktree: null,
      } }),
    }] };
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
    const services = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
      attention,
      initialFocus: { kind: "interrupted_current_node" },
      routes: [{ method: "workflow.task.resume", result: {
        outcome: "applied", applied: { current_nodes: [] },
      } }],
    });
    const user = userEvent.setup();

    await waitFor(() => { expect(scrollIntoView).toHaveBeenCalled(); });
    const focused = scrollIntoView.mock.contexts[0];
    if (!(focused instanceof HTMLElement)) throw new Error("Expected focused attention row.");
    await user.click(within(focused).getByTestId("task-detail-resume"));
    expect(await screen.findByText("task setup failed")).toBeInTheDocument();
    await user.click(screen.getByTestId("setup-recovery-retry"));
    await waitFor(() => { expect(getCallCount(services.transport.calls, "workflow.task.resume")).toBe(1); });
    expect(callParams(services.transport.calls, "workflow.task.resume")).toMatchObject({
      execution_target: { mode: "head" },
    });
  });

  it("surfaces malformed Task-detail recovery contracts", async () => {
    const attention = { ...interruptedTaskAttentionResponse, items: [{
      ...interruptedTaskAttentionResponse.items[0],
      session_name: null,
      detail_json: '{"setup_recovery":{}}',
    }] };
    mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, { attention, initialFocus: { kind: "interrupted_current_node" } });
    expect(await screen.findByRole("alert")).not.toBeEmptyDOMElement();
  });
});

function Harness({
  execute,
  onResult,
  setupRecovery,
}: Readonly<{
  execute: ExecuteStub;
  onResult: (result: TaskInitiatingActionDialogResult) => void;
  setupRecovery?: TaskSetupRecovery;
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
      <TaskInitiatingActionDialogs continuation={controller} onResult={onResult} setupRecovery={setupRecovery === undefined ? undefined : { onClose: vi.fn(), onSubmit: vi.fn(), recovery: setupRecovery }} />
    </>
  );
}

function MoveHarness({ execute }: Readonly<{
  execute(action: TaskInitiatingAction, selection?: WorkflowExecutionTargetSelection): Promise<TaskInitiatingActionResult>;
}>) {
  const controller = useTaskInitiatingActionController({ execute, onApplied: vi.fn(), onAppliedError: vi.fn() });
  const action = moveTaskInitiatingAction({
    taskID: "task-1", targetNodeID: "node-2", commentary: "keep this",
    transitionKey: "next", values: { plan: { summary: "done" } }, proceedDespiteDependencies: true,
  });
  return <>
    <button data-testid="initiate-move" onClick={() => void controller.run(action)} type="button" />
    <TaskInitiatingActionDialogs continuation={controller} onResult={(result) => {
      if (result.kind === "continue") void controller.run(result.action, result.selection);
    }} />
  </>;
}

function retainedSetupError() {
  const root = "/worktrees/task-1";
  return new RpcError({ code: -32039, message: "setup failed", method: "workflow.task.move", data: {
    type: "worktree_setup_retained", script_path: "/repo/setup.sh", diagnostic: "setup failed twice",
    retained_previous_worktree: null, worktree: { variant: "registered", registered: {
      git: { canonical_root: root, head_object: "abc", branch_ref: null, branch_name: null, detached: false, bare: false, locked_reason: null, prunable_reason: null, is_main: false, path_available: true },
      kent: { worktree_id: "worktree-1", canonical_root: root, display_name: "KENT-453", managed: true, created_branch: true, origin_session_id: null },
    } },
  } });
}

function requireMove(action: TaskInitiatingAction): Extract<TaskInitiatingAction, { kind: "move" }> {
  if (action.kind !== "move") throw new Error("Expected Move action.");
  return action;
}

function appliedMove(action: TaskInitiatingAction | undefined): TaskInitiatingActionResult {
  return { kind: "move", action: requireMove(action ?? startTaskInitiatingAction("invalid")), response: {
    outcome: "applied", applied: { currentNodes: [] },
  } };
}

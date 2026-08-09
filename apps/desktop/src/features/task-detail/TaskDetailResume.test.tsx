import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import {
  callParams,
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponseWithInterruptedCurrentScript,
} from "@/test-support/task-detail";
import { parseSetupOperationID } from "@/api";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { TaskResumeButton, TaskResumeProvider } from "./TaskResumeButton";

it("hides Resume when its authority is unavailable", () => {
  const services = createTestServices([]);
  render(
    <TestAppProviders services={services}>
      <TaskResumeProvider authority={{ kind: "unavailable" }} onApplied={() => undefined} taskID="task-1">
        <TaskResumeButton disabled={false} />
      </TaskResumeProvider>
    </TestAppProviders>,
  );

  expect(screen.queryByTestId("task-detail-resume")).not.toBeInTheDocument();
});

it("withholds ordinary Resume until Task attention loads successfully", async () => {
  let finishAttention: ((value: { items: never[]; generated_at_unix_ms: number }) => void) | undefined;
  const attentionResponse = new Promise<{ items: never[]; generated_at_unix_ms: number }>((resolve) => {
    finishAttention = resolve;
  });
  const services = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
    initialFocus: { kind: "dependencies" },
    routes: [
      {
        method: "workflow.task.attention.list",
        handler: async () => attentionResponse,
      },
    ],
  });

  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.attention.list")).toBe(1);
  });
  expect(screen.queryByTestId("task-detail-resume")).not.toBeInTheDocument();

  await act(async () => {
    finishAttention?.({ items: [], generated_at_unix_ms: 3 });
    await attentionResponse;
  });
  expect(await screen.findByTestId("task-detail-resume")).toBeInTheDocument();
});

it("withholds ordinary Resume when Task attention fails", async () => {
  const services = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
    initialFocus: { kind: "dependencies" },
    routes: [
      {
        method: "workflow.task.attention.list",
        error: new Error("attention unavailable"),
      },
    ],
  });

  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.attention.list")).toBe(1);
  });
  expect(screen.queryByTestId("task-detail-resume")).not.toBeInTheDocument();
});

it("reuses one Task Detail Resume continuation for target selection", async () => {
  let resumeCalls = 0;
  const services = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
    routes: [
      {
        method: "workflow.task.resume",
        handler: () => {
          resumeCalls += 1;
          return resumeCalls === 1
            ? {
                outcome: "selection_required",
                selection_required: { reason: "policy_requires_selection" },
              }
            : {
                outcome: "applied",
                applied: {
                  current_nodes: [{ node_id: "node-script", transition_branch_key: null, session_id: null }],
                },
              };
        },
      },
    ],
  });
  const user = userEvent.setup();
  const resumeButtons = await screen.findAllByTestId("task-detail-resume");
  const resumeButton = resumeButtons[0];
  if (resumeButton === undefined) {
    throw new Error("Expected a Task Detail Resume button.");
  }

  await user.click(resumeButton);
  await screen.findByTestId("execution-target-submit");
  const firstRequest = callParams(services.transport.calls, "workflow.task.resume");

  await user.click(screen.getByTestId("execution-target-submit"));
  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.resume")).toBe(2);
  });
  const requests = services.transport.calls
    .filter((call) => call.method === "workflow.task.resume")
    .map((call) => call.params);
  expect(requests[1]).toMatchObject({
    setup_operation_id: firstRequest.setup_operation_id,
    execution_target: { mode: "default_branch" },
  });
});

it("gives only the exact canonical setup interruption a recoverable Resume control", async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const setupOperationID = "55555555-5555-4555-8555-555555555555";
  let finishResume:
    | ((value: {
        outcome: "applied";
        applied: {
          current_nodes: {
            node_id: string;
            transition_branch_key: string;
            session_id: null;
          }[];
        };
      }) => void)
    | undefined;
  const resumeResponse = new Promise<{
    outcome: "applied";
    applied: {
      current_nodes: {
        node_id: string;
        transition_branch_key: string;
        session_id: null;
      }[];
    };
  }>((resolve) => {
    finishResume = resolve;
  });
  const attention = {
    items: [
      {
        id: "generic-sibling",
        kind: "interrupted_current_node",
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        task_id: "task-1",
        task_short_id: "T-1",
        task_title: "Resolve blocker",
        current_node: { node_id: "sibling-node", transition_branch_key: null, session_id: null },
        session_id: null,
        session_name: null,
        detail_json: JSON.stringify({ code: "workflow_runtime_failed", fields: {} }),
        occurred_at_unix_ms: 1,
      },
      {
        id: "canonical-recovery",
        kind: "interrupted_current_node",
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        task_id: "task-1",
        task_short_id: "T-1",
        task_title: "Resolve blocker",
        current_node: {
          node_id: "canonical-node",
          transition_branch_key: "branch-2",
          session_id: null,
        },
        session_id: null,
        session_name: null,
        setup_operation_id: setupOperationID,
        detail_json: JSON.stringify({
          code: "workflow_setup_recovery",
          fields: {},
          setup_recovery: {
            setup_operation_id: setupOperationID,
            cause: "operational",
            diagnostic: "setup failed after retry",
            script_path: "/repo/setup.sh",
            execution_target: { mode: "custom_ref", custom_ref: "refs/heads/dev" },
            retained_worktree: { worktree_id: "worktree-current", root: "/repo/current" },
          },
        }),
        occurred_at_unix_ms: 2,
      },
    ],
    generated_at_unix_ms: 3,
  };
  const services = mountTaskDetailSurface(taskDetailResponseWithInterruptedCurrentScript, {
    attention,
    initialFocus: {
      kind: "interrupted_current_node",
      currentNodeID: "canonical-node",
      currentNodeBranchKey: "branch-2",
      setupOperationID: parseSetupOperationID(setupOperationID),
    },
    routes: [
      {
        method: "workflow.task.resume",
        async handler() {
          return resumeResponse;
        },
      },
    ],
  });
  const user = userEvent.setup();

  const resumeButtons = await screen.findAllByTestId("task-detail-resume");
  expect(resumeButtons).toHaveLength(1);
  const resumeButton = resumeButtons[0];
  if (resumeButton === undefined) {
    throw new Error("Expected canonical setup-recovery Resume.");
  }
  await user.click(resumeButton);
  expect(await screen.findByTestId("setup-recovery-retry")).toBeInTheDocument();
  expect(screen.getByText("/repo/setup.sh")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByTestId("setup-recovery-retry")).not.toBeInTheDocument();
  await user.click(screen.getByTestId("task-detail-resume"));
  await user.click(await screen.findByTestId("setup-recovery-retry"));

  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.resume")).toBe(1);
  });
  expect(screen.getByTestId("setup-recovery-retry")).toBeDisabled();
  await user.click(screen.getByTestId("setup-recovery-retry"));
  expect(getCallCount(services.transport.calls, "workflow.task.resume")).toBe(1);
  const request = callParams(services.transport.calls, "workflow.task.resume");
  expect(request.setup_operation_id).not.toBe(setupOperationID);
  expect(request.execution_target).toEqual({ mode: "custom_ref", custom_ref: "refs/heads/dev" });
  expect(
    services.transport.subscriptionStarts.some(
      (subscription) => subscription.method === "worktree.setup.subscribe",
    ),
  ).toBe(false);
  await act(async () => {
    finishResume?.({
      outcome: "applied",
      applied: {
        current_nodes: [{ node_id: "canonical-node", transition_branch_key: "branch-2", session_id: null }],
      },
    });
    await resumeResponse;
  });
  await waitFor(() => {
    expect(screen.queryByTestId("setup-recovery-retry")).not.toBeInTheDocument();
  });
});

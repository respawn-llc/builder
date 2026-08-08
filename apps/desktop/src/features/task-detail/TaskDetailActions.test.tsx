import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  mountTaskDetailSurface,
  taskDetailResponse,
  taskDetailResponseWithCurrentScript,
} from "@/test-support/task-detail";

function taskWithActions(overrides: Record<string, unknown>) {
  return {
    task: {
      ...taskDetailResponse.task,
      attention_count: 0,
      ...overrides,
    },
  };
}

it("orders Start, Open, and targeted Interrupt in one wrapping action flow", async () => {
  const sessionName = "A session name that is deliberately much too long";
  mountTaskDetailSurface(
    taskWithActions({
      live_sessions: [
        {
          session_id: "session-1",
          session_name: sessionName,
          node_display_name: "Code Review",
        },
      ],
      actions: {
        ...taskDetailResponse.task.actions,
        can_start: true,
      },
    }),
  );

  const flow = await screen.findByTestId("task-detail-action-flow");
  expect(
    within(flow)
      .getAllByRole("button")
      .map((button) => button.textContent),
  ).toEqual([
    "Start",
    `Open ${sessionName.slice(0, 31)}… Chat`,
    `Interrupt ${sessionName.slice(0, 31)}… Chat`,
  ]);
  expect(within(flow).getByRole("button", { name: `Open ${sessionName} Chat` })).toHaveAttribute(
    "title",
    `Open ${sessionName} Chat`,
  );
  expect(within(flow).getByRole("button", { name: `Interrupt ${sessionName} Chat` })).toHaveAttribute(
    "title",
    `Interrupt ${sessionName} Chat`,
  );
});

it("falls back to the Agent Node display name and keeps Task-wide Interrupt generic", async () => {
  mountTaskDetailSurface(
    taskWithActions({
      live_sessions: [
        {
          session_id: "session-1",
          node_display_name: "Implementation",
        },
        {
          session_id: "session-2",
          session_name: "Review",
          node_display_name: "Code Review",
        },
      ],
    }),
  );

  expect(await screen.findByRole("button", { name: "Open Implementation Chat" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Open Review Chat" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Interrupt" })).toBeInTheDocument();
});

it("keeps Interrupt generic when a Script is the live target", async () => {
  mountTaskDetailSurface({
    task: {
      ...taskDetailResponseWithCurrentScript.task,
      actions: {
        ...taskDetailResponseWithCurrentScript.task.actions,
        can_interrupt: true,
      },
    },
  });

  expect(await screen.findByRole("button", { name: "Interrupt" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Interrupt .* Chat/ })).not.toBeInTheDocument();
});

it("disables Start while its request is pending and while disconnected", async () => {
  let resolveStart: ((value: unknown) => void) | undefined;
  const services = mountTaskDetailSurface(
    taskWithActions({
      live_sessions: [],
      actions: {
        ...taskDetailResponse.task.actions,
        can_start: true,
        can_interrupt: false,
      },
    }),
    {
      routes: [
        {
          method: "workflow.task.start",
          handler: async () =>
            new Promise((resolve) => {
              resolveStart = resolve;
            }),
        },
      ],
    },
  );
  const user = userEvent.setup();
  const start = await screen.findByTestId("task-detail-start");

  await user.click(start);
  await waitFor(() => {
    expect(start).toBeDisabled();
  });
  resolveStart?.({
    outcome: "applied",
    applied: {
      current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }],
    },
  });
  await waitFor(() => {
    expect(start).not.toBeDisabled();
  });

  act(() => {
    services.transport.connection.set("disconnected", "offline");
  });
  await waitFor(() => {
    expect(start).toBeDisabled();
  });
});

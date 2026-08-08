import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { appI18n } from "@/i18n";
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
  const sessionName = `${"👨‍👩‍👧‍👦".repeat(31)}e\u0301x`;
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
  const start = within(flow).getByTestId("task-detail-start");
  const openLabel = appI18n.t("task.openChat", { name: sessionName });
  const interruptLabel = appI18n.t("task.interruptChat", { name: sessionName });
  const open = within(flow).getByRole("button", { name: openLabel });
  const interrupt = within(flow).getByRole("button", { name: interruptLabel });
  expect(within(flow).getAllByRole("button")).toEqual([start, open, interrupt]);
  expect(open).toHaveAttribute("title", openLabel);
  expect(interrupt).toHaveAttribute("title", interruptLabel);
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

  const flow = await screen.findByTestId("task-detail-action-flow");
  const firstOpen = within(flow).getByRole("button", {
    name: appI18n.t("task.openChat", { name: "Implementation" }),
  });
  const secondOpen = within(flow).getByRole("button", {
    name: appI18n.t("task.openChat", { name: "Review" }),
  });
  const interrupt = within(flow).getByRole("button", { name: appI18n.t("board.interrupt") });
  expect(within(flow).getAllByRole("button")).toEqual([firstOpen, secondOpen, interrupt]);
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

  const flow = await screen.findByTestId("task-detail-action-flow");
  const interrupt = within(flow).getByRole("button", { name: appI18n.t("board.interrupt") });
  expect(within(flow).getAllByRole("button").at(-1)).toBe(interrupt);
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

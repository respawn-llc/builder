import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { appI18n } from "@/i18n";
import {
  callParams,
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponse,
} from "@/test-support/task-detail";

it("starts the persisted Task without submitting a dirty title or description draft", async () => {
  const services = mountTaskDetailSurface(
    {
      task: {
        ...taskDetailResponse.task,
        current_nodes: [],
        live_sessions: [],
        status: {
          kind: "backlog",
          native_state: "backlog",
          node_ids: [],
          attention_types: [],
        },
        actions: {
          ...taskDetailResponse.task.actions,
          can_start: true,
          can_interrupt: false,
        },
        attention_count: 0,
      },
    },
    {
      routes: [
        {
          method: "workflow.task.start",
          result: {
            outcome: "applied",
            applied: {
              current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }],
            },
          },
        },
      ],
    },
  );
  const user = userEvent.setup();

  const title = await screen.findByRole("textbox", { name: appI18n.t("task.name") });
  await user.clear(title);
  await user.type(title, "Unsaved title");
  await user.click(screen.getByRole("textbox", { name: appI18n.t("task.description") }));
  const description = await screen.findByRole("textbox", { name: appI18n.t("task.description") });
  await user.clear(description);
  await user.type(description, "Unsaved description");

  await user.click(screen.getByTestId("task-detail-start"));

  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.start")).toBe(1);
  });
  expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(0);
  expect(callParams(services.transport.calls, "workflow.task.start")).toMatchObject({
    task_id: "task-1",
    proceed_despite_dependencies: false,
  });
});

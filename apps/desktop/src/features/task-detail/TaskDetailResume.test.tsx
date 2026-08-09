import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  callParams,
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponseWithInterruptedCurrentScript,
} from "@/test-support/task-detail";

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
                  current_nodes: [
                    { node_id: "node-script", transition_branch_key: null, session_id: null },
                  ],
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

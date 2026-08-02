import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  callParams,
  getCallCount,
  mountTaskDetailSurface,
  taskDetailResponseWithInterruptedCurrentScript,
} from "@/test-support/task-detail";

describe("Task Detail Resume actions", () => {
  it("uses the shared continuation for action-panel selection and retry", async () => {
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
                  selection_required: { reason: "unlocked_preparation_failed" },
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
    await screen.findByTestId("task-detail-resume");

    await user.click(screen.getByTestId("task-detail-resume"));
    await screen.findByTestId("execution-target-submit");
    const firstRequest = callParams(services.transport.calls, "workflow.task.resume");
    expect(typeof firstRequest.setup_operation_id).toBe("string");

    await user.click(screen.getByTestId("execution-target-submit"));
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.resume")).toBe(2);
    });
    const resumeRequests = services.transport.calls
      .filter((call) => call.method === "workflow.task.resume")
      .map((call) => call.params);
    expect(resumeRequests[1]).toMatchObject({
      setup_operation_id: firstRequest.setup_operation_id,
    });
  });
});

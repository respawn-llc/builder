import { ApiClient } from "./client";
import { FakeRpcTransport } from "./fakeTransport";

describe("ApiClient workflow script path validation", () => {
  it("maps workflow script path validation", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.scriptPath.validate",
        result: {
          valid: false,
          errors: [
            {
              code: "workflow.validation.script_path_missing",
              message: "script_path is required",
              workflow_id: "workflow-1",
              node_id: "node-script",
              blocks_context: true,
            },
          ],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.validateWorkflowScriptPath({
        workflowID: "workflow-1",
        nodeID: "node-script",
        scriptPath: "scripts/run",
      }),
    ).resolves.toMatchObject({ valid: false });

    expect(transport.calls).toContainEqual({
      method: "workflow.scriptPath.validate",
      params: { workflow_id: "workflow-1", node_id: "node-script", script_path: "scripts/run" },
    });
  });
});

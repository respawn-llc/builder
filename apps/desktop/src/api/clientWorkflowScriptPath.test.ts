import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

describe("ApiClient workflow script path validation", () => {
  it("maps workflow script path validation", async () => {
    const nodeID = "10000000-0000-4000-8000-000000000005";
    const transport = new FakeRpcTransport([
      {
        method: "workflow.scriptPath.validate",
        result: {
          valid: false,
          errors: [
            {
              code: "workflow.validation.script_path_missing",
              message: "script_path is required",
              workflow_id: "11111111-1111-4111-8111-111111111111",
              node_id: nodeID,
              transition_group_id: null,
              edge_id: null,
              details: { provider_edge_id: null },
              blocks_context: true,
            },
          ],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.validateWorkflowScriptPath({
        workflowID: "11111111-1111-4111-8111-111111111111",
        nodeID,
        scriptPath: "scripts/run",
      }),
    ).resolves.toMatchObject({ valid: false });

    expect(transport.calls).toContainEqual({
      method: "workflow.scriptPath.validate",
      params: {
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: nodeID,
        script_path: "scripts/run",
      },
    });
  });
});

import { taskDetailResponse } from "@/test-support/task-detail";
import { taskDetailSchema } from "./schemas/workflowBoard";

describe("task detail execution target contract", () => {
  it("maps durable target facts, recorded worktree path, and current executions", () => {
    const detail = taskDetailSchema.parse(taskDetailResponse);

    expect(detail.executionTarget).toEqual({
      mode: "head",
      requestedRef: "HEAD",
      resolvedRef: "refs/heads/main",
      commitOID: "0123456789abcdef0123456789abcdef01234567",
      provenance: "resolved",
    });
    expect(detail.worktreePath).toBe("/tmp/worktree");
    expect(detail.currentNodes).toEqual([
      {
        effectiveAssignee: null,
        effectiveThinking: null,
        nodeID: "node-1",
        transitionBranchKey: null,
        sessionID: "session-1",
      },
    ]);
    expect(detail.liveSessionIDs).toEqual(["session-1"]);
    expect(detail.currentScripts).toEqual([]);
  });

  it("distinguishes unlocked and source-workspace targets", () => {
    const unlocked = taskDetailSchema.parse(withExecutionTarget(undefined));
    const sourceTarget = taskDetailSchema.parse(
      withExecutionTarget({
        mode: "none",
        provenance: "resolved",
      }),
    );

    expect(unlocked.executionTarget).toBeNull();
    expect(sourceTarget.executionTarget).toEqual({
      mode: "none",
      requestedRef: null,
      resolvedRef: null,
      commitOID: null,
      provenance: "resolved",
    });
  });

  it("rejects removed operational facts and missing required current arrays", () => {
    expect(() =>
      taskDetailSchema.parse(
        withExecutionTarget({
          mode: "head",
          requested_ref: "HEAD",
          commit_oid: "0123456789abcdef0123456789abcdef01234567",
          provenance: "resolved",
          effective_root: "/tmp/worktree",
        }),
      ),
    ).toThrow();

    const withoutSessions = { ...taskDetailResponse.task };
    Reflect.deleteProperty(withoutSessions, "current_nodes");
    expect(() => taskDetailSchema.parse({ task: withoutSessions })).toThrow();

    expect(() =>
      taskDetailSchema.parse({
        task: {
          ...taskDetailResponse.task,
          current_scripts: null,
        },
      }),
    ).toThrow();
  });

  it("accepts session-backed Current Nodes and sessionless Current Scripts", () => {
    const executionIDs = Array.from({ length: 201 }, (_, index) => index.toString());
    const currentNodes = executionIDs.map((executionID) => ({
      node_id: `node-${executionID}`,
      transition_branch_key: null,
      session_id: `session-${executionID}`,
    }));
    const currentScripts = executionIDs.map((executionID) => ({
      current_node: {
        node_id: `script-node-${executionID}`,
        transition_branch_key: null,
        session_id: null,
      },
      path: "script",
    }));

    const detail = taskDetailSchema.parse({
      task: {
        ...taskDetailResponse.task,
        current_nodes: currentNodes,
        current_scripts: currentScripts,
      },
    });

    expect(detail.currentNodes).toEqual(
      currentNodes.map((node) => ({
        effectiveAssignee: null,
        effectiveThinking: null,
        nodeID: node.node_id,
        transitionBranchKey: node.transition_branch_key,
        sessionID: node.session_id,
      })),
    );
    expect(detail.currentScripts).toEqual(
      currentScripts.map((script) => ({
        currentNode: {
          nodeID: script.current_node.node_id,
          transitionBranchKey: script.current_node.transition_branch_key,
          sessionID: script.current_node.session_id,
        },
        path: script.path,
      })),
    );
  });

  it("normalizes an omitted Current Script Session ID to null", () => {
    const detail = taskDetailSchema.parse({
      task: {
        ...taskDetailResponse.task,
        current_scripts: [
          {
            current_node: {
              node_id: "node-script",
              transition_branch_key: null,
            },
            path: "scripts/run",
          },
        ],
      },
    });

    expect(detail.currentScripts[0]?.currentNode.sessionID).toBeNull();
  });

  it("rejects a Current Script with a Session", () => {
    expect(() =>
      taskDetailSchema.parse({
        task: {
          ...taskDetailResponse.task,
          current_scripts: [
            {
              current_node: {
                node_id: "node-script",
                transition_branch_key: null,
                session_id: "session-1",
              },
              path: "scripts/run",
            },
          ],
        },
      }),
    ).toThrow();
  });
});

function withExecutionTarget(executionTarget: unknown) {
  return {
    task: {
      ...taskDetailResponse.task,
      execution_target: executionTarget,
    },
  };
}

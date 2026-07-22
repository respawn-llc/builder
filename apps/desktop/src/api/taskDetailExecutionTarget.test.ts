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
    expect(detail.currentSessionIDs).toEqual(["session-1"]);
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
    Reflect.deleteProperty(withoutSessions, "current_session_ids");
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

  it("accepts every current execution the server contract permits", () => {
    const currentSessionIDs = Array.from({ length: 201 }, (_, index) => `session-${index}`);
    const currentScripts = Array.from({ length: 201 }, (_, index) => ({
      run_id: `run-${index}`,
      path: "script",
    }));

    const detail = taskDetailSchema.parse({
      task: {
        ...taskDetailResponse.task,
        current_session_ids: currentSessionIDs,
        current_scripts: currentScripts,
      },
    });

    expect(detail.currentSessionIDs).toEqual(currentSessionIDs);
    expect(detail.currentScripts).toEqual(
      currentScripts.map((script) => ({
        runID: script.run_id,
        path: script.path,
      })),
    );
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

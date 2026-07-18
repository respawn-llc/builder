import { taskDetailCopyValueNoticePolicy, type TaskDetailCopyValueKind } from "./taskDetailCopyValuePolicy";

describe("task detail copy value notice policy", () => {
  it("selects a distinct total policy for every visible copy value kind", () => {
    const kinds = {
      commit: { kind: "commit" },
      managed_worktree_path: { kind: "managed_worktree_path" },
      source_workspace_path: { kind: "source_workspace_path" },
      transition_output: { kind: "transition_output", outputName: "result" },
    } as const satisfies {
      [Kind in TaskDetailCopyValueKind["kind"]]: Extract<TaskDetailCopyValueKind, { kind: Kind }>;
    };
    const values = Object.values(kinds);
    const policies = values.map(taskDetailCopyValueNoticePolicy);

    expect(new Set(policies.map((policy) => policy.success.id)).size).toBe(values.length);
    expect(new Set(policies.map((policy) => policy.success.titleKey)).size).toBe(values.length);
    expect(new Set(policies.map((policy) => policy.failure.id)).size).toBe(values.length);
    expect(new Set(policies.map((policy) => policy.failure.titleKey)).size).toBe(values.length);
    expect(policies[3]).toMatchObject({
      copyLabel: {
        interpolation: { name: "result" },
      },
      failure: {
        interpolation: { name: "result" },
      },
      success: {
        interpolation: { name: "result" },
      },
    });
  });
});

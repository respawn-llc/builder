import { taskDetailResponse } from "../testSupport/taskDetailFixtures";
import { taskDetailSchema } from "./schemas/workflowBoard";

describe("task detail execution target contract", () => {
  it("maps healthy managed target history and current operational facts", () => {
    const detail = taskDetailSchema.parse(taskDetailResponse);

    expect(detail.executionTarget).toEqual({
      mode: "head",
      effectiveRoot: "/tmp/worktree",
      requestedRef: "HEAD",
      resolvedRef: "refs/heads/main",
      commitOID: "0123456789abcdef0123456789abcdef01234567",
      provenance: "resolved",
      currentBranch: "T-1",
      managedWorktree: {
        id: "worktree-1",
        displayName: "T-1",
        canonicalRoot: "/tmp/worktree",
        availability: "available",
      },
    });
  });

  it("distinguishes unlocked and no-managed-worktree tasks", () => {
    const unlocked = taskDetailSchema.parse(withExecutionTarget(undefined));
    const sourceTarget = taskDetailSchema.parse(
      withExecutionTarget({
        mode: "none",
        effective_root: "/tmp/project",
        provenance: "resolved",
      }),
    );

    expect(unlocked.executionTarget).toBeNull();
    expect(sourceTarget.executionTarget).toEqual({
      mode: "none",
      effectiveRoot: "/tmp/project",
      requestedRef: null,
      resolvedRef: null,
      commitOID: null,
      provenance: "resolved",
      currentBranch: null,
      managedWorktree: null,
    });
  });

  it("accepts missing managed operational facts while retaining locked history", () => {
    const detail = taskDetailSchema.parse(
      withExecutionTarget({
        mode: "custom_ref",
        requested_ref: "release/v2",
        resolved_ref: "refs/tags/release/v2",
        commit_oid: "0123456789abcdef0123456789abcdef01234567",
        provenance: "resolved",
        managed_worktree: {
          worktree_id: "worktree-1",
          display_name: "T-1",
          canonical_root: "/tmp/worktree",
          availability: "missing",
        },
      }),
    );

    expect(detail.executionTarget).toMatchObject({
      effectiveRoot: null,
      currentBranch: null,
      requestedRef: "release/v2",
      managedWorktree: { availability: "missing" },
    });
  });

  it("rejects legacy duplicate worktrees and inconsistent operational facts", () => {
    expect(() =>
      taskDetailSchema.parse({
        ...withExecutionTarget(undefined),
        task: {
          ...withExecutionTarget(undefined).task,
          managed_worktree: { canonical_root: "/legacy" },
        },
      }),
    ).toThrow();
    expect(() =>
      taskDetailSchema.parse(
        withExecutionTarget({
          mode: "head",
          effective_root: "/wrong",
          requested_ref: "HEAD",
          commit_oid: "0123456789abcdef0123456789abcdef01234567",
          provenance: "resolved",
          current_branch: "T-1",
          managed_worktree: {
            worktree_id: "worktree-1",
            display_name: "T-1",
            canonical_root: "/tmp/worktree",
            availability: "available",
          },
        }),
      ),
    ).toThrow();
    expect(() =>
      taskDetailSchema.parse(
        withExecutionTarget({
          mode: "head",
          effective_root: "/tmp/worktree",
          requested_ref: "HEAD",
          commit_oid: "0123456789abcdef0123456789abcdef01234567",
          provenance: "resolved",
          current_branch: "T-1",
          managed_worktree: {
            worktree_id: "worktree-1",
            display_name: "T-1",
            canonical_root: "/tmp/worktree",
            availability: "missing",
          },
        }),
      ),
    ).toThrow();
  });
});

function withExecutionTarget(executionTarget: unknown) {
  const task = { ...taskDetailResponse.task, execution_target: executionTarget };
  return { task };
}

import { describe, expect, it } from "vitest";

import { workflowExecutionPolicySchema } from "./workflow";
import { workflowTaskInitiatingActionResultSchema } from "./workflowLifecycle";

describe("workflow execution policy schemas", () => {
  it("parses every persisted policy and rejects invalid custom-ref combinations", () => {
    expect(
      ["none", "head", "default_branch", "ask"].map((mode) => workflowExecutionPolicySchema.parse({ mode })),
    ).toEqual([
      { customRef: null, mode: "none" },
      { customRef: null, mode: "head" },
      { customRef: null, mode: "default_branch" },
      { customRef: null, mode: "ask" },
    ]);
    expect(
      workflowExecutionPolicySchema.parse({ mode: "custom_ref", custom_ref: "refs/heads/release" }),
    ).toEqual({ customRef: "refs/heads/release", mode: "custom_ref" });
    expect(workflowExecutionPolicySchema.safeParse({ mode: "custom_ref" }).success).toBe(false);
    expect(workflowExecutionPolicySchema.safeParse({ mode: "head", custom_ref: "main" }).success).toBe(false);
  });

  it("parses every initiating-action outcome and target-source variant", () => {
    const sourceVariants = [
      { kind: "non_git" },
      { kind: "named_ref", named_ref: "refs/heads/main", commit: "abc123" },
      { kind: "detached_commit", commit: "abc123" },
      { kind: "unavailable" },
    ];
    for (const source of sourceVariants) {
      expect(
        workflowTaskInitiatingActionResultSchema.parse({
          outcome: "selection_required",
          selection_required: {
            task_id: "task-1",
            generation: "generation-1",
            source_workspace_id: "workspace-1",
            source,
            supported_selections: ["none", "head", "default_branch", "custom_ref"],
            configured_policy: { mode: "ask" },
          },
        }).outcome,
      ).toBe("selection_required");
    }

    expect(
      workflowTaskInitiatingActionResultSchema.parse({
        outcome: "started",
        started: { transition_id: "transition-1", placement_id: "placement-1", run_id: "run-1" },
      }),
    ).toMatchObject({ outcome: "started", started: { runID: "run-1" } });
    expect(
      workflowTaskInitiatingActionResultSchema.parse({
        outcome: "moved",
        moved: { transition_id: "transition-1", state: "approved", placement_ids: [], run_ids: [] },
      }),
    ).toMatchObject({ outcome: "moved", moved: { transitionID: "transition-1" } });
    expect(
      workflowTaskInitiatingActionResultSchema.parse({
        outcome: "approved",
        approved: { transition_id: "transition-1", task_id: "task-1", state: "approved" },
      }),
    ).toMatchObject({ outcome: "approved", approved: { taskID: "task-1" } });
    expect(
      workflowTaskInitiatingActionResultSchema.parse({
        outcome: "in_progress",
        in_progress: { task_id: "task-1", phase: "recovery_queued" },
      }),
    ).toMatchObject({ outcome: "in_progress", inProgress: { phase: "recovery_queued" } });
    expect(
      workflowTaskInitiatingActionResultSchema.parse({
        outcome: "conflict",
        conflict: { task_id: "task-1" },
      }),
    ).toEqual({ conflict: { taskID: "task-1" }, outcome: "conflict" });
  });
});

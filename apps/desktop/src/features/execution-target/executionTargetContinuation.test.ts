import { describe, expect, it } from "vitest";

import { newSetupOperationID } from "../../api/setupOperationID";
import {
  approveExecutionTargetAction,
  initialExecutionTargetSelectionDraft,
  moveExecutionTargetAction,
  startExecutionTargetAction,
} from "./executionTargetContinuation";

describe("execution target continuation model", () => {
  it("defaults policy selection to the repository default branch", () => {
    expect(
      initialExecutionTargetSelectionDraft({ reason: "policy_requires_selection" }),
    ).toEqual({
      mode: "default_branch",
      customRef: "",
    });
  });

  it("preserves a configured custom ref when that fixed target is unavailable", () => {
    expect(
      initialExecutionTargetSelectionDraft({
        reason: "configured_target_unavailable",
        configuredTarget: { mode: "custom_ref", requestedRef: "release/v2" },
        unavailableCause: "invalid_revision",
      }),
    ).toEqual({
      mode: "custom_ref",
      customRef: "release/v2",
    });
  });

  it("retains the exact initiating action and stable setup operation id for every action kind", () => {
    const startSetupID = newSetupOperationID();
    const moveSetupID = newSetupOperationID();
    const approveSetupID = newSetupOperationID();

    expect(startExecutionTargetAction("task-1", startSetupID)).toEqual({
      kind: "start",
      taskID: "task-1",
      setupOperationID: startSetupID,
    });
    expect(
      moveExecutionTargetAction({
        taskID: "task-1",
        targetNodeID: "node-1",
        setupOperationID: moveSetupID,
      }),
    ).toEqual({
      kind: "move",
      input: {
        taskID: "task-1",
        targetNodeID: "node-1",
        setupOperationID: moveSetupID,
      },
    });
    expect(approveExecutionTargetAction("transition-1", approveSetupID)).toEqual({
      kind: "approve",
      taskTransitionID: "transition-1",
      setupOperationID: approveSetupID,
    });
  });
});

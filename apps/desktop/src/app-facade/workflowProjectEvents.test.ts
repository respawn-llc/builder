import { describe, expect, it } from "vitest";

import type { WorkflowProjectEvent } from "@/api";
import {
  workflowProjectEventAffectsTask,
  workflowProjectEventCanChangeAttention,
  workflowProjectQuestionTaskID,
} from "./workflowProjectEvents";

describe("workflow project event helpers", () => {
  it("recognizes resources that can affect attention", () => {
    expect(workflowProjectEventCanChangeAttention(workflowEvent({ resource: "task" }))).toBe(true);
    expect(workflowProjectEventCanChangeAttention(workflowEvent({ resource: "workflow_link" }))).toBe(true);
    expect(workflowProjectEventCanChangeAttention(workflowEvent({ resource: "runtime_log" }))).toBe(false);
  });

  it("extracts task ids from question lifecycle events", () => {
    expect(
      workflowProjectQuestionTaskID(
        workflowEvent({
          action: "question_waiting",
          changedIDs: ["task-1", "run-1", "ask-1"],
          resource: "task",
        }),
      ),
    ).toBe("task-1");
    expect(
      workflowProjectQuestionTaskID(
        workflowEvent({ action: "completed", changedIDs: ["task-1"], resource: "task" }),
      ),
    ).toBeNull();
    expect(
      workflowProjectQuestionTaskID(
        workflowEvent({ action: "question_waiting", changedIDs: [], resource: "task" }),
      ),
    ).toBeNull();
  });

  it("matches any task-resource event whose changed ids include the task", () => {
    const cases = [
      "created",
      "updated",
      "started",
      "moved",
      "completed",
      "comment_added",
      "question_waiting",
    ];
    for (const action of cases) {
      expect(
        workflowProjectEventAffectsTask(
          workflowEvent({ action, changedIDs: ["task-1", "run-1"], resource: "task" }),
          "task-1",
        ),
      ).toBe(true);
    }
  });

  it("ignores events for other tasks, other resources, or a blank task id", () => {
    expect(
      workflowProjectEventAffectsTask(workflowEvent({ changedIDs: ["task-2"], resource: "task" }), "task-1"),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask(
        workflowEvent({ changedIDs: ["task-1"], resource: "workflow" }),
        "task-1",
      ),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask(workflowEvent({ changedIDs: ["task-1"], resource: "task" }), "   "),
    ).toBe(false);
  });
});

function workflowEvent(overrides: Partial<WorkflowProjectEvent>): WorkflowProjectEvent {
  return {
    action: "updated",
    changedIDs: ["task-1"],
    occurredAtUnixMs: 1,
    projectID: "project-1",
    resource: "task",
    workflowID: "workflow-1",
    ...overrides,
  };
}

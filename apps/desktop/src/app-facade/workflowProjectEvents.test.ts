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
    expect(workflowProjectEventCanChangeAttention(workflowEvent({ resource: "label" }))).toBe(false);
  });

  it("extracts task ids from question lifecycle events", () => {
    expect(
      workflowProjectQuestionTaskID(
        workflowEvent({
          action: "question_waiting",
          primaryEntityID: "task-1",
          relatedIDs: ["run-1", "ask-1"],
          resource: "task",
        }),
      ),
    ).toBe("task-1");
    expect(
      workflowProjectQuestionTaskID(
        workflowEvent({ action: "completed", primaryEntityID: "task-1", resource: "task" }),
      ),
    ).toBeNull();
  });

  it("matches any task-resource event whose primary entity is the task", () => {
    const cases = [
      "created",
      "updated",
      "started",
      "moved",
      "completed",
      "comment_added",
      "question_waiting",
    ] as const;
    for (const action of cases) {
      expect(
        workflowProjectEventAffectsTask(
          workflowEvent({ action, primaryEntityID: "task-1", relatedIDs: ["run-1"], resource: "task" }),
          "task-1",
        ),
      ).toBe(true);
    }
  });

  it("matches task label assignment events", () => {
    expect(
      workflowProjectEventAffectsTask(
        workflowEvent({
          action: "labels_changed",
          primaryEntityID: "task-1",
          resource: "task",
        }),
        "task-1",
      ),
    ).toBe(true);
  });

  it("ignores events for other tasks, other resources, or a blank task id", () => {
    expect(
      workflowProjectEventAffectsTask(
        workflowEvent({ primaryEntityID: "task-2", resource: "task" }),
        "task-1",
      ),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask(
        workflowEvent({
          action: "updated",
          primaryEntityID: "workflow-1",
          projectID: null,
          resource: "workflow",
        }),
        "task-1",
      ),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask(workflowEvent({ primaryEntityID: "task-1", resource: "task" }), "   "),
    ).toBe(false);
  });
});

function workflowEvent(overrides: Partial<WorkflowProjectEvent>): WorkflowProjectEvent {
  return {
    action: "updated",
    occurredAtUnixMs: 1,
    primaryEntityID: "task-1",
    projectID: "project-1",
    relatedIDs: [],
    resource: "task",
    workflowID: "workflow-1",
    ...overrides,
  };
}

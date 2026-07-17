import { describe, expect, it } from "vitest";

import {
  workflowProjectEvent,
  workflowProjectEventAffectsTask,
  workflowProjectEventCanChangeAttention,
  workflowProjectQuestionTaskID,
} from "./workflowProjectEvents";

describe("workflowProjectEvent", () => {
  it("parses workflow project event params", () => {
    expect(
      workflowProjectEvent({
        event: {
          action: "question_waiting",
          changed_ids: ["task-1", "run-1", "ask-1"],
          project_id: "project-1",
          resource: "task",
          workflow_id: "workflow-1",
        },
      }),
    ).toEqual({
      action: "question_waiting",
      changedIDs: ["task-1", "run-1", "ask-1"],
      projectID: "project-1",
      resource: "task",
      workflowID: "workflow-1",
    });
  });

  it("recognizes resources that can affect attention", () => {
    expect(workflowProjectEventCanChangeAttention({ event: { resource: "task" } })).toBe(true);
    expect(workflowProjectEventCanChangeAttention({ event: { resource: "workflow_link" } })).toBe(true);
    expect(workflowProjectEventCanChangeAttention({ event: { resource: "runtime_log" } })).toBe(false);
    expect(workflowProjectEventCanChangeAttention({})).toBe(false);
  });

  it("extracts task ids from question lifecycle events", () => {
    expect(
      workflowProjectQuestionTaskID({
        event: {
          action: "question_waiting",
          changed_ids: ["task-1", "run-1", "ask-1"],
          resource: "task",
        },
      }),
    ).toBe("task-1");
    expect(
      workflowProjectQuestionTaskID({
        event: { action: "completed", changed_ids: ["task-1"], resource: "task" },
      }),
    ).toBeNull();
    expect(
      workflowProjectQuestionTaskID({
        event: { action: "question_waiting", changed_ids: [], resource: "task" },
      }),
    ).toBeNull();
  });
});

describe("workflowProjectEventAffectsTask", () => {
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
          { event: { action, changed_ids: ["task-1", "run-1"], resource: "task" } },
          "task-1",
        ),
      ).toBe(true);
    }
  });

  it("ignores events for other tasks, other resources, or a blank task id", () => {
    expect(
      workflowProjectEventAffectsTask({ event: { changed_ids: ["task-2"], resource: "task" } }, "task-1"),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask({ event: { changed_ids: ["task-1"], resource: "workflow" } }, "task-1"),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsTask({ event: { changed_ids: ["task-1"], resource: "task" } }, "   "),
    ).toBe(false);
    expect(workflowProjectEventAffectsTask({}, "task-1")).toBe(false);
  });
});

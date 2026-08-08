import type { TaskDetail, WorkflowProjectEvent } from "@/api";
import {
  optimisticTaskDependencyRemoval,
  requiredTaskDependencyDirection,
  workflowProjectEventAffectsDependencyDetail,
  workflowProjectEventAffectsDependencyBoard,
} from "./index";

function taskEvent(
  action: WorkflowProjectEvent["action"],
  workflowID: string,
  primaryEntityID: string,
  relatedIDs: readonly string[] = [],
): WorkflowProjectEvent {
  return {
    action,
    occurredAtUnixMs: 1,
    primaryEntityID,
    projectID: "project-1",
    relatedIDs,
    resource: "task",
    workflowID,
  };
}

describe("task dependency presentation authority", () => {
  it("provides the canonical required direction projection", () => {
    const dependencies = detailFixture().dependencies;

    expect(requiredTaskDependencyDirection(dependencies, "blocked-by")).toBe(dependencies.directions[0]);
    expect(() =>
      requiredTaskDependencyDirection(
        { ...dependencies, directions: dependencies.directions.slice(1) },
        "blocked-by",
      ),
    ).toThrow();
  });

  it("optimistically removes only the open task direction using server satisfaction", () => {
    const detail = detailFixture();
    const updated = optimisticTaskDependencyRemoval(detail, {
      blockerTaskID: "blocker-1",
      blockedTaskID: "task-1",
    });

    expect(updated.dependencies).toMatchObject({
      blockerCount: 0,
      unsatisfiedBlockerCount: 0,
      directlyBlockedTaskCount: 1,
    });
    expect(updated.dependencies.directions[0]?.items).toEqual([]);
    expect(updated.dependencies.directions[1]?.items).toHaveLength(1);
  });

  it.each(["dependencies_changed", "deleted", "moved", "approved", "completed"] as const)(
    "refreshes a board for foreign workflow dependency-impact action %s",
    (action) => {
      expect(
        workflowProjectEventAffectsDependencyBoard(
          taskEvent(action, "workflow-foreign", "blocker-1"),
          "workflow-board",
        ),
      ).toBe(true);
    },
  );

  it.each([
    "comment_added",
    "labels_changed",
    "question_waiting",
    "interrupted",
    "resumed",
    "updated",
  ] as const)("does not refresh a board for unrelated foreign action %s", (action) => {
    expect(
      workflowProjectEventAffectsDependencyBoard(
        taskEvent(action, "workflow-foreign", "blocker-1"),
        "workflow-board",
      ),
    ).toBe(false);
  });

  it("refreshes related-row detail actions without broad dependency invalidation", () => {
    const relatedIDs = new Set(["blocker-1"]);
    expect(
      workflowProjectEventAffectsDependencyDetail(
        taskEvent("updated", "workflow-foreign", "blocker-1"),
        "task-1",
        relatedIDs,
      ),
    ).toBe(true);
    expect(
      workflowProjectEventAffectsDependencyDetail(
        taskEvent("comment_added", "workflow-foreign", "blocker-1"),
        "task-1",
        relatedIDs,
      ),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsDependencyDetail(
        taskEvent("dependencies_changed", "workflow-foreign", "blocker-1", ["third-task"]),
        "task-1",
        relatedIDs,
      ),
    ).toBe(false);
    expect(
      workflowProjectEventAffectsDependencyDetail(
        taskEvent("dependencies_changed", "workflow-foreign", "blocker-1", ["task-1"]),
        "task-1",
        relatedIDs,
      ),
    ).toBe(true);
  });
});

function detailFixture(): TaskDetail {
  return {
    id: "task-1",
    shortID: "KENT-1",
    projectID: "project-1",
    projectName: "Kent",
    workflowID: "workflow-board",
    workflowName: "Board",
    workflowVersion: 1,
    title: "Ship",
    body: "",
    sourceURL: "",
    sourceWorkspace: {
      id: "workspace-1",
      name: "Main",
      rootPath: "/workspace",
      availability: "available",
      isPrimary: true,
      updatedAt: 1,
    },
    status: { kind: "backlog", nativeState: "active", nodeIDs: [], attentionTypes: [] },
    actions: {
      canStart: true,
      canInterrupt: false,
      canResume: false,
      canDelete: true,
    },
    labelIDs: [],
    attentionCount: 0,
    dependencies: {
      blockerCount: 1,
      unsatisfiedBlockerCount: 1,
      directlyBlockedTaskCount: 1,
      directions: [
        {
          direction: "blocked-by",
          totalCount: 1,
          unsatisfiedCount: 1,
          addAvailability: { kind: "available", remainingCapacity: 4 },
          items: [
            {
              taskID: "blocker-1",
              shortID: "KENT-2",
              title: "Prepare",
              workflowID: "workflow-foreign",
              satisfaction: "unsatisfied",
              status: { kind: "backlog", nativeState: "active", nodeIDs: [], attentionTypes: [] },
            },
          ],
        },
        {
          direction: "blocks",
          totalCount: 1,
          unsatisfiedCount: null,
          addAvailability: { kind: "available", remainingCapacity: 3 },
          items: [
            {
              taskID: "blocked-1",
              shortID: "KENT-3",
              title: "Publish",
              workflowID: "workflow-foreign",
              satisfaction: null,
              status: { kind: "active", nativeState: "active", nodeIDs: [], attentionTypes: [] },
            },
          ],
        },
      ],
    },
    executionTarget: null,
    worktreePath: null,
    currentNodes: [],
    liveSessions: [],
    currentScripts: [],
    retainedSessionCount: 0,
    createdAt: 1,
    updatedAt: 1,
    done: false,
  };
}

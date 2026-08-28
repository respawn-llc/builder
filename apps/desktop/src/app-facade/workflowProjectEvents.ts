import type { WorkflowProjectEvent } from "@/api";

export function workflowProjectQuestionTaskID(event: WorkflowProjectEvent): string | null {
  if (event.resource !== "task" || !questionActions.has(event.action)) {
    return null;
  }
  return event.primaryEntityID;
}

export function workflowProjectEventCanChangeTaskSearch(event: WorkflowProjectEvent): boolean {
  return event.resource === "task" || (event.resource === "workflow" && event.action === "deleted");
}

// workflowProjectEventAffectsTask reports whether a project event mutates the
// given task in a way that changes its detail representation. The server emits
// every task-affecting action with the task as its typed primary entity, so
// callers do not depend on positional related IDs.
export function workflowProjectEventAffectsTask(event: WorkflowProjectEvent, taskID: string): boolean {
  const trimmedTaskID = taskID.trim();
  if (trimmedTaskID.length === 0) {
    return false;
  }
  return event.resource === "task" && event.primaryEntityID === trimmedTaskID;
}

const questionActions = new Set(["question_waiting", "question_cleared"]);

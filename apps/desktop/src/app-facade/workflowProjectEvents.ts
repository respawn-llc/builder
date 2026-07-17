import type { WorkflowProjectEvent } from "@/api";

export function workflowProjectEventCanChangeAttention(event: WorkflowProjectEvent): boolean {
  return attentionResources.has(event.resource);
}

export function workflowProjectQuestionTaskID(event: WorkflowProjectEvent): string | null {
  if (event.resource !== "task" || !questionActions.has(event.action)) {
    return null;
  }
  return event.changedIDs[0] ?? null;
}

// workflowProjectEventAffectsTask reports whether a project event mutates the
// given task in a way that changes its detail representation. The server emits
// every task-affecting action (created/updated/started/interrupted/resumed/
// approved/moved/canceled/completed/comment_*/question_*) as a "task" resource
// event whose first changed id is the task id, so a structured resource +
// changed-id match reliably covers all of them without enumerating actions.
export function workflowProjectEventAffectsTask(event: WorkflowProjectEvent, taskID: string): boolean {
  const trimmedTaskID = taskID.trim();
  if (trimmedTaskID.length === 0) {
    return false;
  }
  return event.resource === "task" && event.changedIDs.includes(trimmedTaskID);
}

const attentionResources = new Set(["task", "workflow", "workflow_link"]);
const questionActions = new Set(["question_waiting", "question_cleared", "question_answered"]);

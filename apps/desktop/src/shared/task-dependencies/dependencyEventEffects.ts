import type { WorkflowProjectEvent } from "@/api";

const foreignBoardDependencyActions = new Set<WorkflowProjectEvent["action"]>([
  "dependencies_changed",
  "deleted",
  "moved",
  "approved",
  "completed",
]);

const relatedDetailActions = new Set<WorkflowProjectEvent["action"]>([
  "updated",
  "deleted",
  "started",
  "interrupted",
  "resumed",
  "approved",
  "moved",
  "completed",
  "question_waiting",
  "question_cleared",
  "question_answered",
]);

export function workflowProjectEventAffectsDependencyBoard(
  event: WorkflowProjectEvent,
  selectedWorkflowID: string | undefined,
): boolean {
  if (event.resource === "workflow_link") {
    return true;
  }
  if (event.resource === "workflow") {
    return event.action === "deleted" || event.workflowID === null || event.workflowID === selectedWorkflowID;
  }
  if (event.resource !== "task") {
    return false;
  }
  if (event.workflowID === null || event.workflowID === selectedWorkflowID) {
    return true;
  }
  return foreignBoardDependencyActions.has(event.action);
}

export function workflowProjectEventAffectsDependencyDetail(
  event: WorkflowProjectEvent,
  openTaskID: string,
  relatedTaskIDs: ReadonlySet<string>,
): boolean {
  if (event.resource === "workflow") {
    return event.action === "deleted";
  }
  if (event.resource !== "task") {
    return false;
  }
  if (event.action === "dependencies_changed") {
    return event.primaryEntityID === openTaskID || event.relatedIDs.includes(openTaskID);
  }
  if (event.primaryEntityID === openTaskID) {
    return true;
  }
  return relatedTaskIDs.has(event.primaryEntityID) && relatedDetailActions.has(event.action);
}

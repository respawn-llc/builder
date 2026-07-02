import { z } from "zod";

export type WorkflowProjectEvent = Readonly<{
  action: string;
  changedIDs: readonly string[];
  projectID: string;
  resource: string;
  workflowID: string;
}>;

const stringSchema = z.string();

const workflowProjectEventParamsSchema = z.object({
  event: z.object({
    action: z.string().catch(""),
    changed_ids: z
      .array(z.unknown())
      .transform((items) => items.flatMap(stringArrayItem))
      .catch([]),
    project_id: z.string().catch(""),
    resource: z.string().catch(""),
    workflow_id: z.string().catch(""),
  }),
});

export function workflowProjectEvent(params: unknown): WorkflowProjectEvent | null {
  const parsed = workflowProjectEventParamsSchema.safeParse(params);
  if (!parsed.success) {
    return null;
  }
  const rawEvent = parsed.data.event;
  return {
    action: rawEvent.action,
    changedIDs: rawEvent.changed_ids,
    projectID: rawEvent.project_id,
    resource: rawEvent.resource,
    workflowID: rawEvent.workflow_id,
  };
}

function stringArrayItem(item: unknown): readonly string[] {
  const parsed = stringSchema.safeParse(item);
  return parsed.success ? [parsed.data] : [];
}

export function workflowProjectEventCanChangeAttention(params: unknown): boolean {
  const event = workflowProjectEvent(params);
  return event !== null && attentionResources.has(event.resource);
}

export function workflowProjectQuestionTaskID(params: unknown): string | null {
  const event = workflowProjectEvent(params);
  if (event?.resource !== "task" || !questionActions.has(event.action)) {
    return null;
  }
  const taskID = event.changedIDs[0] ?? "";
  return taskID.length > 0 ? taskID : null;
}

// workflowProjectEventAffectsTask reports whether a project event mutates the
// given task in a way that changes its detail representation. The server emits
// every task-affecting action (created/updated/started/interrupted/resumed/
// approved/moved/canceled/completed/comment_*/question_*) as a "task" resource
// event whose first changed id is the task id, so a structured resource +
// changed-id match reliably covers all of them without enumerating actions.
export function workflowProjectEventAffectsTask(params: unknown, taskID: string): boolean {
  const trimmedTaskID = taskID.trim();
  if (trimmedTaskID.length === 0) {
    return false;
  }
  const event = workflowProjectEvent(params);
  return event !== null && event.resource === "task" && event.changedIDs.includes(trimmedTaskID);
}

const attentionResources = new Set(["task", "workflow", "workflow_link"]);
const questionActions = new Set(["question_waiting", "question_cleared", "question_answered"]);

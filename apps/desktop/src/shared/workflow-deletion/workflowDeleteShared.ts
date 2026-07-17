import type { WorkflowDeleteImpact, WorkflowDeleteInput } from "@/api";

export const workflowDeleteNativeDialogPath = "/native-dialog/workflow-delete";
export const workflowDeleteDialogWidth = 460;

export type WorkflowDeleteTarget = Readonly<{
  impact: WorkflowDeleteImpact;
}>;

export function workflowDeleteInputFromImpact(impact: WorkflowDeleteImpact): WorkflowDeleteInput {
  return {
    confirmed: true,
    expectedLinkCount: impact.linkCount,
    expectedProjectCount: impact.projectCount,
    expectedTaskCount: impact.taskCount,
    expectedVersion: impact.version,
    workflowID: impact.workflowID,
  };
}

export function workflowDeleteBlockersMessage(
  blockers: readonly { message: string }[],
  fallback: string,
): string {
  const messages = blockers.map((blocker) => blocker.message).filter((message) => message.length > 0);
  return messages.length === 0 ? fallback : messages.join("\n");
}

export function workflowDeleteWindowOptions(impact: WorkflowDeleteImpact, title: string) {
  return {
    initialHeight: 300,
    initialWidth: workflowDeleteDialogWidth,
    label: `workflow-delete-${impact.workflowID}`,
    params: workflowDeleteSearchParams(impact),
    route: workflowDeleteNativeDialogPath,
    title,
  };
}

function workflowDeleteSearchParams(impact: WorkflowDeleteImpact): Readonly<Record<string, string>> {
  return {
    active_run_count: impact.activeRunCount.toString(),
    blocked_task_count: impact.blockedTaskCount.toString(),
    default_replacement_project_count: impact.defaultReplacementProjectCount.toString(),
    link_count: impact.linkCount.toString(),
    project_count: impact.projectCount.toString(),
    runnable_run_count: impact.runnableRunCount.toString(),
    task_count: impact.taskCount.toString(),
    version: impact.version.toString(),
    workflow_id: impact.workflowID,
  };
}

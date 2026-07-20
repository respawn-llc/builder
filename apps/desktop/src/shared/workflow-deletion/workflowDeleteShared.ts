import type { WorkflowDeleteImpact, WorkflowDeleteInput } from "@/api";

export const workflowDeleteDialogWidth = 460;

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

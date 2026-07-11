import type { TaskStatus } from "../../api";
import type { BadgeTone } from "../../ui";

export function taskStatusTone(status: TaskStatus): BadgeTone {
  if (status.kind === "canceled" || status.nativeState === "canceled") {
    return "danger";
  }
  if (status.kind === "done") {
    return "success";
  }
  if (status.kind === "running" || status.kind === "queued" || status.nativeState === "running") {
    return "info";
  }
  if (isInterruptedOrFailed(status)) {
    return "danger";
  }
  if (isWaitingOrAttention(status)) {
    return "warning";
  }
  return "neutral";
}

function isInterruptedOrFailed(status: TaskStatus): boolean {
  return (
    status.kind === "interrupted" || status.nativeState === "interrupted" || status.nativeState === "failed"
  );
}

function isWaitingOrAttention(status: TaskStatus): boolean {
  return (
    status.kind === "waiting_approval" ||
    status.kind === "waiting_question" ||
    status.nativeState === "waiting_approval" ||
    status.nativeState === "waiting_ask" ||
    status.attentionTypes.length > 0 ||
    status.runIDs.length > 0
  );
}

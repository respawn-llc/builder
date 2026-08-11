import type { AttentionItem } from "@/api";
import type { TaskDetailInitialFocus } from "./sidebarContext";

export function taskDetailInitialFocusFromAttentionItem(
  item: AttentionItem | undefined,
): TaskDetailInitialFocus | undefined {
  if (item?.kind === "question" && item.question.promptID.length > 0) {
    return { kind: "question", askIDs: [item.question.promptID] };
  }
  if (item?.kind === "approval" && item.approvalID.length > 0) {
    return { kind: "approval", approvalID: item.approvalID };
  }
  if (item?.kind === "interrupted_current_node") {
    return { kind: "interrupted_current_node" };
  }
  return undefined;
}

export function taskDetailInitialFocusRequestKey(taskID: string, focus: TaskDetailInitialFocus): string {
  return `${taskID}:${taskDetailInitialFocusSegment(focus)}`;
}

export function sameTaskDetailInitialFocus(
  left: TaskDetailInitialFocus | null,
  right: TaskDetailInitialFocus,
): boolean {
  if (left?.kind !== right.kind) {
    return false;
  }
  if (left.kind === "question") {
    return (
      right.kind === "question" &&
      left.askIDs.length === right.askIDs.length &&
      left.askIDs.every((askID, index) => askID === right.askIDs[index])
    );
  }
  if (left.kind === "approval") {
    return right.kind === "approval" && left.approvalID === right.approvalID;
  }
  return right.kind === left.kind;
}

function taskDetailInitialFocusSegment(focus: TaskDetailInitialFocus): string {
  if (focus.kind === "question") {
    return `question:${focus.askIDs.join(",")}`;
  }
  if (focus.kind === "approval") {
    return `approval:${focus.approvalID}`;
  }
  return focus.kind;
}

import type { AttentionItem } from "@/api";
import type { TaskDetailInitialFocus } from "./sidebarContext";

export function taskDetailInitialFocusFromAttentionItem(
  item: AttentionItem | undefined,
): TaskDetailInitialFocus | undefined {
  if (item?.kind === "question" && item.questionID.length > 0) {
    return { kind: "question", askIDs: [item.questionID] };
  }
  if (item?.kind === "approval" && item.approvalID.length > 0) {
    return { kind: "approval", approvalID: item.approvalID };
  }
  if (item?.kind === "interrupted_current_node") {
    return {
      kind: "interrupted_current_node",
      currentNodeID: item.currentNode.nodeID,
      currentNodeBranchKey: item.currentNode.transitionBranchKey,
      setupOperationID: item.setupOperationID,
    };
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
  return sameTaskDetailFocusByKind(left, right);
}

function sameTaskDetailFocusByKind(left: TaskDetailInitialFocus, right: TaskDetailInitialFocus): boolean {
  switch (left.kind) {
    case "question":
      return right.kind === left.kind && sameStrings(left.askIDs, right.askIDs);
    case "approval":
      return right.kind === left.kind && left.approvalID === right.approvalID;
    case "interrupted_current_node":
      return (
        right.kind === left.kind &&
        left.currentNodeID === right.currentNodeID &&
        left.currentNodeBranchKey === right.currentNodeBranchKey &&
        left.setupOperationID?.toJSONValue() === right.setupOperationID?.toJSONValue()
      );
    case "dependencies":
      return right.kind === left.kind;
  }
}

function sameStrings(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function taskDetailInitialFocusSegment(focus: TaskDetailInitialFocus): string {
  if (focus.kind === "question") {
    return `question:${focus.askIDs.join(",")}`;
  }
  if (focus.kind === "approval") {
    return `approval:${focus.approvalID}`;
  }
  if (focus.kind === "interrupted_current_node") {
    return [
      focus.kind,
      focus.currentNodeID,
      focus.currentNodeBranchKey ?? "serial",
      focus.setupOperationID?.toJSONValue() ?? "ordinary",
    ].join(":");
  }
  return focus.kind;
}

import type { AttentionItem } from "../api";
import type { TaskDetailInitialFocus } from "./sidebarContext";

export function taskDetailInitialFocusFromAttentionItem(
  item: AttentionItem | undefined,
): TaskDetailInitialFocus | undefined {
  if (item?.kind === "question" && item.askID.length > 0) {
    return { kind: "question", askIDs: [item.askID] };
  }
  if (item?.kind === "approval" && item.taskTransitionID.length > 0) {
    return { kind: "approval", taskTransitionID: item.taskTransitionID };
  }
  if (item?.kind === "interrupted_run" && item.runID.length > 0) {
    return { kind: "interrupted_run", runID: item.runID };
  }
  return undefined;
}

export function taskDetailInitialFocusRequestKey(
  taskID: string,
  focus: TaskDetailInitialFocus,
): string {
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
    return right.kind === "approval" && left.taskTransitionID === right.taskTransitionID;
  }
  return right.kind === "interrupted_run" && left.runID === right.runID;
}

function taskDetailInitialFocusSegment(focus: TaskDetailInitialFocus): string {
  if (focus.kind === "question") {
    return `question:${focus.askIDs.join(",")}`;
  }
  if (focus.kind === "approval") {
    return `approval:${focus.taskTransitionID}`;
  }
  return `interrupted_run:${focus.runID}`;
}

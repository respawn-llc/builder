import type { AttentionItem } from "@/api";

export function taskDetailAttentionRowKey(item: AttentionItem): string {
  if (item.kind !== "question") {
    return item.id;
  }
  return [item.question.sessionID, item.question.stepID, item.question.toolCallID]
    .map((part) => `${String(part.length)}:${part}`)
    .join("");
}

import type { AttentionItem } from "@/api";
import type { SessionChatTarget } from "@/app-facade";

export function attentionChatTarget(item: AttentionItem): SessionChatTarget | null {
  switch (item.kind) {
    case "question":
      return { projectID: item.projectID, sessionID: item.question.sessionID };
    case "interrupted_current_node":
      return item.sessionID === null ? null : { projectID: item.projectID, sessionID: item.sessionID };
    case "approval":
      return null;
  }
}

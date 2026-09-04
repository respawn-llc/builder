import type { AttentionItem } from "@/api";

export type AttentionChatTarget = Readonly<{
  projectID: string;
  sessionID: string;
}>;

export function attentionChatTarget(item: AttentionItem): AttentionChatTarget | null {
  switch (item.kind) {
    case "question":
      return { projectID: item.projectID, sessionID: item.question.sessionID };
    case "interrupted_current_node":
      return item.sessionID === null ? null : { projectID: item.projectID, sessionID: item.sessionID };
    case "approval":
      return null;
  }
}

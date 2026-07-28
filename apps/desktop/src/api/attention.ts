import type { ApprovalSnapshot, AttentionQuestionPrompt, TaskCurrentNode } from "./models";

type AttentionItemBase = Readonly<{
  id: string;
  projectID: string;
  workflowID: string;
  taskID: string;
  taskShortID: string;
  taskTitle: string;
  message: string;
  occurredAt: number;
}>;

export type QuestionAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "question";
    currentNode: TaskCurrentNode;
    sessionID: string | null;
    questionID: string;
    suggestions: readonly string[];
    recommendedOptionIndex: number | null;
    question: AttentionQuestionPrompt | null;
  }>;

export type ApprovalAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "approval";
    approvalID: string;
    approvalSnapshot: ApprovalSnapshot;
  }>;

export type InterruptedCurrentNodeAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "interrupted_current_node";
    currentNode: TaskCurrentNode;
    sessionID: string | null;
    detailJSON: string | null;
  }>;

export type AttentionItem = QuestionAttentionItem | ApprovalAttentionItem | InterruptedCurrentNodeAttentionItem;

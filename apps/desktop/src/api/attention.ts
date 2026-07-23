import type { ApprovalSnapshot, AttentionQuestionPrompt } from "./models";

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
    runID: string;
    sessionID: string | null;
    askID: string;
    suggestions: readonly string[];
    recommendedOptionIndex: number | null;
    question: AttentionQuestionPrompt | null;
  }>;

export type ApprovalAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "approval";
    taskTransitionID: string;
    approvalSnapshot: ApprovalSnapshot;
  }>;

export type InterruptedRunAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "interrupted_run";
    runID: string;
    sessionID: string | null;
    detailJSON: string | null;
  }>;

export type AttentionItem = QuestionAttentionItem | ApprovalAttentionItem | InterruptedRunAttentionItem;

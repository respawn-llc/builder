import type { ApprovalSnapshot, AttentionQuestionPrompt, TaskCurrentNode } from "./models";
import type { SetupOperationID } from "./setupOperationID";
import type { WorkflowExecutionTargetSelection } from "./workflowExecutionTarget";

export type TaskSetupRecoveryRetainedWorktree = Readonly<{
  worktreeID: string;
  root: string;
}>;

export type TaskSetupRecovery = Readonly<{
  setupOperationID: SetupOperationID;
  cause: "process_exit" | "timeout" | "target_preparation" | "operational";
  diagnostic: string;
  executionTarget: WorkflowExecutionTargetSelection;
  retainedWorktree: TaskSetupRecoveryRetainedWorktree | null;
  retainedPreviousWorktree: TaskSetupRecoveryRetainedWorktree | null;
}>;

type AttentionItemBase = Readonly<{
  id: string;
  projectID: string;
  workflowID: string;
  taskID: string;
  taskShortID: string;
  taskTitle: string;
  occurredAt: number;
}>;

export type QuestionAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "question";
    currentNode: TaskCurrentNode;
    sessionID: string | null;
    sessionName: string | null;
    questionID: string;
    message: string;
    suggestions: readonly string[];
    recommendedOptionIndex: number | null;
    question: AttentionQuestionPrompt | null;
  }>;

export type ApprovalAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "approval";
    approvalID: string;
    approvalSnapshot: ApprovalSnapshot;
    message: string | null;
  }>;

export type InterruptedCurrentNodeAttentionItem = AttentionItemBase &
  Readonly<{
    kind: "interrupted_current_node";
    currentNode: TaskCurrentNode;
    sessionID: string | null;
    detailJSON: string | null;
    message: string | null;
    setupOperationID: SetupOperationID | null;
    setupRecovery: TaskSetupRecovery | null;
  }>;

export type AttentionItem =
  QuestionAttentionItem | ApprovalAttentionItem | InterruptedCurrentNodeAttentionItem;

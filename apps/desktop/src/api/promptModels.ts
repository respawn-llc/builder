import type { ApprovalDecision } from "./models";

export type PromptIdentity = Readonly<{
  promptID: string;
  sessionID: string;
  stepID: string;
}>;

export type OrdinaryQuestionPrompt = PromptIdentity &
  Readonly<{
    kind: "ordinary";
    suggestions: readonly string[];
    recommendedOptionIndex: number | null;
  }>;

export type ApprovalQuestionPrompt = PromptIdentity &
  Readonly<{
    kind: "approval";
    approvalDecisions: readonly ApprovalDecision[];
  }>;

export type AttentionQuestionPrompt = OrdinaryQuestionPrompt | ApprovalQuestionPrompt;

import type { ApprovalDecision } from "./models";

export type PromptIdentity = Readonly<{
  toolCallID: string;
  sessionID: string;
  stepID: string;
}>;

export type FileAccessTarget = Readonly<{ requestedPath: string; resolvedPath: string }>;

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
    accessTargets: readonly FileAccessTarget[];
  }>;

export type AttentionQuestionPrompt = OrdinaryQuestionPrompt | ApprovalQuestionPrompt;

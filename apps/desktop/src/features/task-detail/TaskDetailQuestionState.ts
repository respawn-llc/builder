import type { ApprovalDecision, QuestionAttentionItem } from "@/api";
import type { OrdinaryQuestionPrompt } from "@/api";

export type QuestionSelectionProvenance = "uninitialized" | "anchored-default" | "explicit";

export type QuestionSelectionState = Readonly<{
  answer: string;
  approvalDecision: ApprovalDecision | null;
  provenance: QuestionSelectionProvenance;
  selectedOption: number | null;
}>;

export type QuestionSelectionDefault =
  | Readonly<{
      kind: "ordinary";
      selectedOption: number | null;
    }>
  | Readonly<{
      approvalDecision: ApprovalDecision | null;
      kind: "approval";
    }>;

export type QuestionPresentation = Readonly<{
  defaultSelection: QuestionSelectionDefault | null;
  kind: "ordinary" | "approval";
  question: string | undefined;
  recommendedOption: number | null;
  suggestions: readonly string[];
}>;

const emptySuggestions: readonly string[] = [];

export function emptyQuestionSelection(): QuestionSelectionState {
  return {
    answer: "",
    approvalDecision: null,
    provenance: "uninitialized",
    selectedOption: null,
  };
}

export function questionPresentation(attention: QuestionAttentionItem): QuestionPresentation {
  if (attention.question.kind === "approval") {
    return approvalQuestionPresentation(attention.message, attention.question.approvalDecisions);
  }
  return ordinaryQuestionPresentation(attention.message, attention.question);
}

function approvalQuestionPresentation(
  question: string | undefined,
  decisions: readonly ApprovalDecision[],
): QuestionPresentation {
  return {
    defaultSelection: approvalQuestionSelectionDefault(decisions),
    kind: "approval",
    question,
    recommendedOption: null,
    suggestions: emptySuggestions,
  };
}

function ordinaryQuestionPresentation(
  question: string,
  prompt: OrdinaryQuestionPrompt,
): QuestionPresentation {
  const suggestions = prompt.suggestions;
  const recommendation = prompt.recommendedOptionIndex;
  return {
    defaultSelection: ordinaryQuestionSelectionDefault(suggestions.length, recommendation),
    kind: "ordinary",
    question,
    recommendedOption: recommendedOptionNumber(suggestions.length, recommendation),
    suggestions,
  };
}

export function anchorQuestionSelection(
  selection: QuestionSelectionState,
  defaultSelection: QuestionSelectionDefault | null,
): QuestionSelectionState {
  if (selection.provenance !== "uninitialized" || defaultSelection === null) {
    return selection;
  }
  if (defaultSelection.kind === "ordinary") {
    return {
      ...selection,
      approvalDecision: null,
      provenance: "anchored-default",
      selectedOption: defaultSelection.selectedOption,
    };
  }
  return {
    ...selection,
    approvalDecision: defaultSelection.approvalDecision,
    provenance: "anchored-default",
    selectedOption: null,
  };
}

export function withQuestionCommentary(
  selection: QuestionSelectionState,
  answer: string,
): QuestionSelectionState {
  return {
    ...selection,
    answer,
  };
}

export function withOrdinaryQuestionOption(
  selection: QuestionSelectionState,
  selectedOption: number | null,
): QuestionSelectionState {
  return {
    ...selection,
    approvalDecision: null,
    provenance: "explicit",
    selectedOption,
  };
}

export function withApprovalQuestionDecision(
  selection: QuestionSelectionState,
  approvalDecision: ApprovalDecision | null,
): QuestionSelectionState {
  return {
    ...selection,
    approvalDecision,
    provenance: "explicit",
    selectedOption: null,
  };
}

function ordinaryQuestionSelectionDefault(
  suggestionCount: number,
  recommendation: number | null,
): QuestionSelectionDefault {
  return {
    kind: "ordinary",
    selectedOption:
      suggestionCount === 0 ? null : (recommendedOptionNumber(suggestionCount, recommendation) ?? 1),
  };
}

function approvalQuestionSelectionDefault(decisions: readonly ApprovalDecision[]): QuestionSelectionDefault {
  return {
    approvalDecision: decisions.find((decision) => decision === "allow_once") ?? decisions[0] ?? null,
    kind: "approval",
  };
}

function recommendedOptionNumber(suggestionCount: number, recommendation: number | null): number | null {
  return recommendation !== null &&
    Number.isInteger(recommendation) &&
    recommendation >= 1 &&
    recommendation <= suggestionCount
    ? recommendation
    : null;
}

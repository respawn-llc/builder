import type { ApprovalDecision, PendingAsk, QuestionAttentionItem } from "@/api";

export type QuestionSelectionProvenance = "uninitialized" | "anchored-default" | "explicit";

export type QuestionSelectionState = Readonly<{
  answer: string;
  askID: string;
  approvalDecision: ApprovalDecision | null;
  clientRequestID: string | null;
  provenance: QuestionSelectionProvenance;
  selectedOption: number | null;
  submission: "idle" | "submitting" | "accepted";
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

export function emptyQuestionSelection(askID: string): QuestionSelectionState {
  return {
    answer: "",
    approvalDecision: null,
    askID,
    clientRequestID: null,
    provenance: "uninitialized",
    selectedOption: null,
    submission: "idle",
  };
}

export function questionPresentation(
  attention: QuestionAttentionItem,
  pendingAsk: PendingAsk | undefined,
  pendingAskSettled: boolean,
): QuestionPresentation {
  const question = attention.message.length > 0 ? attention.message : pendingAsk?.question;
  if (attention.question?.kind === "approval") {
    return approvalQuestionPresentation(question, attention.question.approvalDecisions);
  }
  return ordinaryQuestionPresentation(attention, pendingAsk, pendingAskSettled, question);
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
  attention: QuestionAttentionItem,
  pendingAsk: PendingAsk | undefined,
  pendingAskSettled: boolean,
  question: string | undefined,
): QuestionPresentation {
  const suggestions =
    attention.suggestions.length > 0
      ? attention.suggestions
      : (pendingAsk?.suggestions ?? emptySuggestions);
  const recommendation =
    attention.suggestions.length > 0
      ? attention.recommendedOptionIndex
      : (pendingAsk?.recommendedOptionIndex ?? null);
  const ready = attention.suggestions.length > 0 || pendingAskSettled;
  return {
    defaultSelection: ready ? ordinaryQuestionSelectionDefault(suggestions.length, recommendation) : null,
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
    clientRequestID: null,
    submission: "idle",
  };
}

export function withOrdinaryQuestionOption(
  selection: QuestionSelectionState,
  selectedOption: number | null,
): QuestionSelectionState {
  return {
    ...selection,
    approvalDecision: null,
    clientRequestID: null,
    provenance: "explicit",
    selectedOption,
    submission: "idle",
  };
}

export function withApprovalQuestionDecision(
  selection: QuestionSelectionState,
  approvalDecision: ApprovalDecision | null,
): QuestionSelectionState {
  return {
    ...selection,
    approvalDecision,
    clientRequestID: null,
    provenance: "explicit",
    selectedOption: null,
    submission: "idle",
  };
}

function ordinaryQuestionSelectionDefault(
  suggestionCount: number,
  recommendation: number | null,
): QuestionSelectionDefault {
  return {
    kind: "ordinary",
    selectedOption:
      suggestionCount === 0
        ? null
        : (recommendedOptionNumber(suggestionCount, recommendation) ?? 1),
  };
}

function approvalQuestionSelectionDefault(
  decisions: readonly ApprovalDecision[],
): QuestionSelectionDefault {
  return {
    approvalDecision: decisions.find((decision) => decision === "allow_once") ?? decisions[0] ?? null,
    kind: "approval",
  };
}

function recommendedOptionNumber(
  suggestionCount: number,
  recommendation: number | null,
): number | null {
  return recommendation !== null &&
    Number.isInteger(recommendation) &&
    recommendation >= 1 &&
    recommendation <= suggestionCount
    ? recommendation
    : null;
}

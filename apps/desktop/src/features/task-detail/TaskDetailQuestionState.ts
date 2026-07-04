import type { ApprovalDecision } from "../../api";

export type QuestionSelectionState = Readonly<{
  answer: string;
  askID: string;
  approvalDecision: ApprovalDecision | null;
  selectedOption: number | null;
  submitted: boolean;
  userSelected: boolean;
}>;

export function emptyQuestionSelection(askID: string): QuestionSelectionState {
  return {
    answer: "",
    approvalDecision: null,
    askID,
    selectedOption: null,
    submitted: false,
    userSelected: false,
  };
}

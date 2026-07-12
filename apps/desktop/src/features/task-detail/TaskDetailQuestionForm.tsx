import { type ReactNode, useId } from "react";
import { useTranslation } from "react-i18next";

import type { ApprovalDecision, AttentionItem, PendingAsk } from "../../api";
import { useOpenExternalLink } from "../../app/nativeHooks";
import { Button, Island, MarkdownText, RadioGroup, RadioGroupItem } from "../../ui";
import { cx } from "../../ui/classes";
import { fieldInputClassName } from "../../ui/fieldInputStyles";
import { emptyQuestionSelection, type QuestionSelectionState } from "./TaskDetailQuestionState";
import { usePendingAsks, type useTaskMutations } from "./useTaskDetailData";

const emptySuggestions: readonly string[] = [];
const neitherRadioValue = "neither";

export function QuestionBox({
  attention,
  disabled,
  mutations,
  selectionState,
  onSelectionStateChange,
  taskId,
}: Readonly<{
  attention: AttentionItem;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  selectionState: QuestionSelectionState;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  taskId: string;
}>) {
  const { t } = useTranslation();
  const isApprovalPrompt = attention.question?.kind === "approval";
  const asks = usePendingAsks(isApprovalPrompt ? null : attention.sessionID);
  const pendingAsk = asks.data?.find((ask) => ask.askID === attention.askID);
  const questionView = taskQuestionView(attention, pendingAsk);

  return (
    <Island aria-label={t("task.question")} className="p-[var(--space-4)]" level={1} radius="l" unpadded>
      <QuestionForm
        answerQuestion={mutations.answerQuestion}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        question={questionView.question}
        recommendedOption={questionView.recommendedOption}
        selectionState={selectionState}
        suggestions={questionView.suggestions}
        taskId={taskId}
      />
    </Island>
  );
}

function QuestionForm({
  answerQuestion,
  attention,
  disabled,
  onSelectionStateChange,
  question,
  recommendedOption,
  selectionState,
  suggestions,
  taskId,
}: Readonly<{
  answerQuestion: ReturnType<typeof useTaskMutations>["answerQuestion"];
  attention: AttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  recommendedOption: number | null;
  selectionState: QuestionSelectionState;
  suggestions: readonly string[];
  taskId: string;
}>) {
  const approvalDecisions = attention.question?.kind === "approval" ? attention.question.approvalDecisions : null;
  if (approvalDecisions !== null) {
    return (
      <ApprovalQuestionForm
        answerQuestion={answerQuestion}
        approvalDecisions={approvalDecisions}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        question={question}
        selectionState={selectionState}
        taskId={taskId}
      />
    );
  }
  return (
    <OrdinaryQuestionForm
      answerQuestion={answerQuestion}
      attention={attention}
      disabled={disabled}
      onSelectionStateChange={onSelectionStateChange}
      question={question}
      recommendedOption={recommendedOption}
      selectionState={selectionState}
      suggestions={suggestions}
      taskId={taskId}
    />
  );
}

function OrdinaryQuestionForm({
  answerQuestion,
  attention,
  disabled,
  onSelectionStateChange,
  question,
  recommendedOption,
  selectionState,
  suggestions,
  taskId,
}: Readonly<{
  answerQuestion: ReturnType<typeof useTaskMutations>["answerQuestion"];
  attention: AttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  recommendedOption: number | null;
  selectionState: QuestionSelectionState;
  suggestions: readonly string[];
  taskId: string;
}>) {
  const { t } = useTranslation();
  const openLink = useOpenExternalLink();
  const selection = selectionForAsk(selectionState, attention.askID);
  const selectedOption = selection.userSelected ? selection.selectedOption : recommendedOption;
  const answer = selection.answer;
  const answerID = useId();
  // A real option can submit on its own; otherwise any typed freeform answer is
  // submittable, including freeform-only asks where no option is selected.
  const canSubmit = (selectedOption !== null && selectedOption > 0) || answer.trim().length > 0;
  const interactionDisabled = disabled || answerQuestion.isPending || selection.submitted;
  const selectedNeither = selection.userSelected && selectedOption === null;
  const radioValue = selectedNeither ? neitherRadioValue : selectedOption === null ? "" : suggestionRadioValue(selectedOption);

  async function submit(): Promise<void> {
    await answerQuestion.mutateAsync({
      kind: "ordinary",
      clientRequestID: questionClientRequestID(attention.askID),
      taskID: taskId,
      runID: attention.runID,
      askID: attention.askID,
      selectedOptionNumber: selectedOption,
      freeformAnswer: answer,
    });
    onSelectionStateChange({
      answer: "",
      approvalDecision: null,
      askID: attention.askID,
      selectedOption: null,
      submitted: true,
      userSelected: true,
    });
  }

  return (
    <QuestionFormFrame
      answer={answer}
      answerID={answerID}
      canSubmit={canSubmit}
      interactionDisabled={interactionDisabled}
      onAnswerChange={(nextAnswer) => {
        onSelectionStateChange({
          answer: nextAnswer,
          approvalDecision: null,
          askID: attention.askID,
          selectedOption,
          submitted: false,
          userSelected: selection.userSelected,
        });
      }}
      onRadioValueChange={(value) => {
        onSelectionStateChange({
          answer,
          approvalDecision: null,
          askID: attention.askID,
          selectedOption: selectedOptionFromRadioValue(value, suggestions),
          submitted: false,
          userSelected: true,
        });
      }}
      onSubmit={submit}
      optionGroup={
        suggestions.length > 0 ? (
          <>
            {suggestions.map((suggestion, optionIndex) => (
              <QuestionOption
                disabled={interactionDisabled}
                key={`${optionIndex.toString()}:${suggestion}`}
                onOpenLink={openLink}
                recommended={recommendedOption === optionIndex + 1}
                text={suggestion}
                value={suggestionRadioValue(optionIndex + 1)}
              />
            ))}
            <QuestionOption
              disabled={interactionDisabled}
              onOpenLink={openLink}
              recommended={false}
              text={t("task.neitherOption")}
              value={neitherRadioValue}
            />
          </>
        ) : undefined
      }
      question={question}
      radioValue={radioValue}
    />
  );
}

function suggestionRadioValue(optionNumber: number): string {
  return `suggestion:${optionNumber.toString()}`;
}

function selectedOptionFromRadioValue(value: string, suggestions: readonly string[]): number | null {
  if (value === neitherRadioValue) {
    return null;
  }
  const optionIndex = suggestions.findIndex((_suggestion, index) => suggestionRadioValue(index + 1) === value);
  if (optionIndex < 0) {
    throw new Error(`Unknown ordinary-question radio value: ${value}`);
  }
  return optionIndex + 1;
}

function ApprovalQuestionForm({
  answerQuestion,
  approvalDecisions,
  attention,
  disabled,
  onSelectionStateChange,
  question,
  selectionState,
  taskId,
}: Readonly<{
  answerQuestion: ReturnType<typeof useTaskMutations>["answerQuestion"];
  approvalDecisions: readonly ApprovalDecision[];
  attention: AttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  selectionState: QuestionSelectionState;
  taskId: string;
}>) {
  const { t } = useTranslation();
  const openLink = useOpenExternalLink();
  const selection = selectionForAsk(selectionState, attention.askID);
  const selectedDecision = selectedApprovalDecisionFor(approvalDecisions, selection);
  const answer = selection.answer;
  const answerID = useId();
  const canSubmit = selectedDecision !== null && (selectedDecision !== "deny" || answer.trim().length > 0);
  const interactionDisabled = disabled || answerQuestion.isPending || selection.submitted;

  async function submit(): Promise<void> {
    if (selectedDecision === null) {
      return;
    }
    await answerQuestion.mutateAsync({
      kind: "approval",
      clientRequestID: questionClientRequestID(attention.askID),
      taskID: taskId,
      runID: attention.runID,
      askID: attention.askID,
      decision: selectedDecision,
      commentary: answer,
    });
    onSelectionStateChange({
      answer: "",
      approvalDecision: null,
      askID: attention.askID,
      selectedOption: null,
      submitted: true,
      userSelected: true,
    });
  }

  return (
    <QuestionFormFrame
      answer={answer}
      answerID={answerID}
      canSubmit={canSubmit}
      interactionDisabled={interactionDisabled}
      onAnswerChange={(nextAnswer) => {
        onSelectionStateChange({
          answer: nextAnswer,
          approvalDecision: selectedDecision,
          askID: attention.askID,
          selectedOption: null,
          submitted: false,
          userSelected: selection.userSelected,
        });
      }}
      onRadioValueChange={(value) => {
        onSelectionStateChange({
          answer,
          approvalDecision: approvalDecisionForValue(approvalDecisions, value),
          askID: attention.askID,
          selectedOption: null,
          submitted: false,
          userSelected: true,
        });
      }}
      onSubmit={submit}
      optionGroup={approvalDecisions.map((decision) => (
        <QuestionOption
          disabled={interactionDisabled}
          key={decision}
          onOpenLink={openLink}
          recommended={false}
          text={approvalDecisionLabel(decision, t)}
          value={decision}
        />
      ))}
      question={question}
      radioValue={selectedDecision ?? ""}
    />
  );
}

function QuestionFormFrame({
  answer,
  answerID,
  canSubmit,
  interactionDisabled,
  onAnswerChange,
  onRadioValueChange,
  onSubmit,
  optionGroup,
  question,
  radioValue,
}: Readonly<{
  answer: string;
  answerID: string;
  canSubmit: boolean;
  interactionDisabled: boolean;
  onAnswerChange: (answer: string) => void;
  onRadioValueChange: (value: string) => void;
  onSubmit: () => Promise<void>;
  optionGroup?: ReactNode;
  question: string | undefined;
  radioValue: string;
}>) {
  const { t } = useTranslation();
  const openLink = useOpenExternalLink();
  const submitDisabled = interactionDisabled || !canSubmit;
  return (
    <form
      className="grid gap-[var(--space-2)]"
      onSubmit={(event) => {
        event.preventDefault();
        if (canSubmit && !interactionDisabled) {
          void onSubmit();
        }
      }}
    >
      {question !== undefined && question.length > 0 ? (
        <div className="min-w-0 text-[var(--color-on-island)]">
          <MarkdownText onOpenLink={openLink} value={question} />
        </div>
      ) : null}
      {optionGroup === undefined ? null : (
        <fieldset className="m-0 border-0 p-0">
          <legend className="sr-only">{t("task.optionNumber")}</legend>
          <RadioGroup
            aria-label={t("task.optionNumber")}
            disabled={interactionDisabled}
            onValueChange={onRadioValueChange}
            value={radioValue}
          >
            {optionGroup}
          </RadioGroup>
        </fieldset>
      )}
      <textarea
        aria-label={t("task.commentary")}
        className={cx(fieldInputClassName, "min-h-24")}
        disabled={interactionDisabled}
        id={answerID}
        onChange={(event) => {
          onAnswerChange(event.target.value);
        }}
        placeholder={t("task.answerPlaceholder")}
        rows={3}
        value={answer}
      />
      <Button disabled={submitDisabled} type="submit" variant="primary">
        {t("task.submitAnswer")}
      </Button>
    </form>
  );
}

function QuestionOption({
  disabled,
  onOpenLink,
  recommended,
  text,
  value,
}: Readonly<{
  disabled: boolean;
  onOpenLink: (url: string) => void;
  recommended: boolean;
  text: string;
  value: string;
}>) {
  const { t } = useTranslation();
  const id = useId();
  return (
    <div
      className={cx(
        "flex items-start gap-[var(--space-2)] text-left text-[var(--color-on-island)]",
        disabled && "opacity-60",
      )}
    >
      <RadioGroupItem className="mt-1" disabled={disabled} id={id} value={value} />
      <label
        className={cx(
          "min-w-0 flex-1 cursor-pointer",
          recommended && "font-bold text-[var(--color-primary)]",
        )}
        htmlFor={id}
      >
        <MarkdownText inline onOpenLink={onOpenLink} value={text} />
        {recommended ? (
          <span className="ml-[var(--space-2)] text-xs font-bold">({t("task.recommended")})</span>
        ) : null}
      </label>
    </div>
  );
}

function recommendedOptionNumber(
  suggestions: readonly string[],
  recommendedOptionIndex: number,
): number | null {
  return recommendedOptionIndex >= 1 && recommendedOptionIndex <= suggestions.length
    ? recommendedOptionIndex
    : null;
}

function taskQuestionView(
  attention: AttentionItem,
  pendingAsk: PendingAsk | undefined,
): Readonly<{ question: string | undefined; suggestions: readonly string[]; recommendedOption: number | null }> {
  const question = attention.message.length > 0 ? attention.message : pendingAsk?.question;
  const suggestions =
    attention.question?.kind === "approval"
      ? emptySuggestions
      : attention.suggestions.length > 0
        ? attention.suggestions
        : (pendingAsk?.suggestions ?? emptySuggestions);
  const recommendedOptionSource =
    attention.suggestions.length > 0
      ? attention.recommendedOptionIndex
      : (pendingAsk?.recommendedOptionIndex ?? 0);
  return {
    question,
    suggestions,
    recommendedOption: recommendedOptionNumber(suggestions, recommendedOptionSource),
  };
}

function questionClientRequestID(askID: string): string {
  return `gui-question-${askID}-${Date.now().toString()}`;
}

function selectionForAsk(selection: QuestionSelectionState, askID: string): QuestionSelectionState {
  return selection.askID === askID ? selection : emptyQuestionSelection(askID);
}

function selectedApprovalDecisionFor(
  decisions: readonly ApprovalDecision[],
  selection: QuestionSelectionState,
): ApprovalDecision | null {
  if (selection.userSelected) {
    return approvalDecisionForValue(decisions, selection.approvalDecision);
  }
  return decisions[0] ?? null;
}

function approvalDecisionForValue(
  decisions: readonly ApprovalDecision[],
  value: string | null,
): ApprovalDecision | null {
  if (value === null) {
    return null;
  }
  return decisions.find((decision) => decision === value) ?? null;
}

function approvalDecisionLabel(decision: ApprovalDecision, t: ReturnType<typeof useTranslation>["t"]): string {
  switch (decision) {
    case "allow_once":
      return t("task.approvalDecisionAllowOnce");
    case "allow_session":
      return t("task.approvalDecisionAllowSession");
    case "deny":
      return t("task.approvalDecisionDeny");
  }
  return decision;
}

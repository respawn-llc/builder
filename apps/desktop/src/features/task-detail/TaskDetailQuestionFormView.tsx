import { type MouseEvent, type ReactNode, useId, useRef } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type ApprovalDecision, type QuestionAttentionItem } from "@/api";
import type { QuestionAnswerInput } from "@/api";
import { useTextFieldSubmitShortcut } from "@/app-facade";
import { Button, RadioGroup, RadioGroupItem, showStatusToast, StaticMarkdown } from "@/ui";
import { cx, fieldInputClassName } from "@/ui";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import {
  withApprovalQuestionDecision,
  withOrdinaryQuestionOption,
  withQuestionCommentary,
  type QuestionPresentation,
  type QuestionSelectionState,
} from "./TaskDetailQuestionState";

const neitherRadioValue = "neither";

export function QuestionFormView({
  answerQuestion,
  attention,
  disabled,
  onSelectionStateChange,
  presentation,
  selectionState,
  taskId,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  presentation: QuestionPresentation;
  selectionState: QuestionSelectionState;
  taskId: string;
}>) {
  const approvalDecisions =
    attention.question?.kind === "approval" ? attention.question.approvalDecisions : null;
  if (approvalDecisions !== null) {
    return (
      <ApprovalQuestionForm
        answerQuestion={answerQuestion}
        approvalDecisions={approvalDecisions}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        question={presentation.question}
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
      question={presentation.question}
      recommendedOption={presentation.recommendedOption}
      selectionState={selectionState}
      suggestions={presentation.suggestions}
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
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  recommendedOption: number | null;
  selectionState: QuestionSelectionState;
  suggestions: readonly string[];
  taskId: string;
}>) {
  const { t } = useTranslation();
  const selection = selectionState;
  const selectedOption = selection.selectedOption;
  const answer = selection.answer;
  const answerID = useId();
  // A real option can submit on its own; otherwise any typed freeform answer is
  // submittable, including freeform-only asks where no option is selected.
  const canSubmit = (selectedOption !== null && selectedOption > 0) || answer.trim().length > 0;
  const interactionDisabled = disabled || answerQuestion.isPending || selection.submission !== "idle";
  const selectedNeither = selection.provenance === "explicit" && selectedOption === null;
  const radioValue = selectedNeither
    ? neitherRadioValue
    : selectedOption === null
      ? ""
      : suggestionRadioValue(selectedOption);

  async function submit(): Promise<void> {
    await submitQuestionAnswer({
      answerQuestion,
      attention,
      failureTitle: t("states.error"),
      input: () => ({
        kind: "ordinary",
        taskID: taskId,
        askID: attention.questionID,
        selectedOptionNumber: selectedOption,
        freeformAnswer: answer,
      }),
      onSelectionStateChange,
      selection,
    });
  }

  return (
    <QuestionFormFrame
      answer={answer}
      answerID={answerID}
      canSubmit={canSubmit}
      interactionDisabled={interactionDisabled}
      onAnswerChange={(nextAnswer) => {
        onSelectionStateChange(withQuestionCommentary(selection, nextAnswer));
      }}
      onRadioValueChange={(value) => {
        onSelectionStateChange(
          withOrdinaryQuestionOption(selection, selectedOptionFromRadioValue(value, suggestions)),
        );
      }}
      onSubmit={submit}
      optionGroup={
        suggestions.length > 0 ? (
          <>
            {suggestions.map((suggestion, optionIndex) => (
              <QuestionOption
                disabled={interactionDisabled}
                key={`${optionIndex.toString()}:${suggestion}`}
                recommended={recommendedOption === optionIndex + 1}
                text={suggestion}
                value={suggestionRadioValue(optionIndex + 1)}
              />
            ))}
            <QuestionOption
              disabled={interactionDisabled}
              recommended={false}
              text={t("task.neitherOption")}
              value={neitherRadioValue}
            />
          </>
        ) : undefined
      }
      question={question}
      radioValue={radioValue}
      submitting={selection.submission === "submitting"}
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
  const optionIndex = suggestions.findIndex(
    (_suggestion, index) => suggestionRadioValue(index + 1) === value,
  );
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
  answerQuestion: QuestionAnswerMutation;
  approvalDecisions: readonly ApprovalDecision[];
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  selectionState: QuestionSelectionState;
  taskId: string;
}>) {
  const { t } = useTranslation();
  const selection = selectionState;
  const selectedDecision = selectedApprovalDecisionFor(approvalDecisions, selection);
  const answer = selection.answer;
  const answerID = useId();
  const canSubmit = selectedDecision !== null && (selectedDecision !== "deny" || answer.trim().length > 0);
  const interactionDisabled = disabled || answerQuestion.isPending || selection.submission !== "idle";

  async function submit(): Promise<void> {
    if (selectedDecision === null) {
      return;
    }
    await submitQuestionAnswer({
      answerQuestion,
      attention,
      failureTitle: t("states.error"),
      input: () => ({
        kind: "approval",
        taskID: taskId,
        askID: attention.questionID,
        decision: selectedDecision,
        commentary: answer,
      }),
      onSelectionStateChange,
      selection,
    });
  }

  return (
    <QuestionFormFrame
      answer={answer}
      answerID={answerID}
      canSubmit={canSubmit}
      interactionDisabled={interactionDisabled}
      onAnswerChange={(nextAnswer) => {
        onSelectionStateChange(withQuestionCommentary(selection, nextAnswer));
      }}
      onRadioValueChange={(value) => {
        onSelectionStateChange(
          withApprovalQuestionDecision(selection, approvalDecisionForValue(approvalDecisions, value)),
        );
      }}
      onSubmit={submit}
      optionGroup={approvalDecisions.map((decision) => (
        <QuestionOption
          disabled={interactionDisabled}
          key={decision}
          recommended={false}
          text={approvalDecisionLabel(decision, t)}
          value={decision}
        />
      ))}
      question={question}
      radioValue={selectedDecision ?? ""}
      submitting={selection.submission === "submitting"}
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
  submitting,
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
  submitting: boolean;
}>) {
  const { t } = useTranslation();
  const submitDisabled = interactionDisabled || !canSubmit;
  const formShortcut = useTextFieldSubmitShortcut({
    available: !submitDisabled,
    kind: "form",
  });
  return (
    <form
      className="grid gap-[var(--space-2)]"
      onKeyDown={formShortcut}
      onSubmit={(event) => {
        event.preventDefault();
        if (canSubmit && !interactionDisabled) {
          void onSubmit();
        }
      }}
    >
      {question !== undefined && question.length > 0 ? (
        <div className="min-w-0 text-[var(--color-on-island)]">
          <StaticMarkdown value={question} />
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
      <Button aria-busy={submitting} disabled={submitDisabled} type="submit" variant="primary">
        {submitting ? t("task.submittingAnswer") : t("task.submitAnswer")}
      </Button>
    </form>
  );
}

function QuestionOption({
  disabled,
  recommended,
  text,
  value,
}: Readonly<{
  disabled: boolean;
  recommended: boolean;
  text: string;
  value: string;
}>) {
  const { t } = useTranslation();
  const id = useId();
  const radioRef = useRef<HTMLButtonElement | null>(null);
  const labelID = `${id}-label`;
  return (
    <div
      className={cx(
        "flex items-start gap-[var(--space-2)] text-left text-[var(--color-on-island)]",
        disabled && "opacity-60",
      )}
      onClick={(event) => {
        if (!disabled) activateRadioFromOption(event, radioRef);
      }}
    >
      <RadioGroupItem
        aria-labelledby={labelID}
        className="mt-1"
        disabled={disabled}
        id={id}
        ref={radioRef}
        value={value}
      />
      <div
        className={cx(
          "min-w-0 flex-1 cursor-pointer",
          recommended && "font-bold text-[var(--color-primary)]",
        )}
        id={labelID}
      >
        <StaticMarkdown value={text} />
        {recommended ? (
          <span className="ml-[var(--space-2)] text-xs font-bold">({t("task.recommended")})</span>
        ) : null}
      </div>
    </div>
  );
}

function activateRadioFromOption(
  event: MouseEvent<HTMLDivElement>,
  radioRef: Readonly<{ current: HTMLButtonElement | null }>,
): void {
  let element = event.target instanceof Element ? event.target : null;
  while (element !== null && element !== event.currentTarget) {
    if (
      element instanceof HTMLAnchorElement ||
      element instanceof HTMLButtonElement ||
      element.getAttribute("role") === "link"
    ) {
      return;
    }
    element = element.parentElement;
  }
  radioRef.current?.click();
}

async function submitQuestionAnswer({
  answerQuestion,
  attention,
  failureTitle,
  input,
  onSelectionStateChange,
  selection,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  failureTitle: string;
  input: () => QuestionAnswerInput;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  selection: QuestionSelectionState;
}>): Promise<void> {
  const submittingSelection = { ...selection, submission: "submitting" as const };
  onSelectionStateChange(submittingSelection);
  try {
    await answerQuestion.mutateAsync(input());
    onSelectionStateChange({ ...submittingSelection, submission: "accepted" });
  } catch (error: unknown) {
    onSelectionStateChange({ ...submittingSelection, submission: "idle" });
    showStatusToast({
      body: errorMessage(error),
      id: `task-question-answer-failed:${attention.questionID}`,
      title: failureTitle,
      tone: "danger",
    });
  }
}

function selectedApprovalDecisionFor(
  decisions: readonly ApprovalDecision[],
  selection: QuestionSelectionState,
): ApprovalDecision | null {
  return approvalDecisionForValue(decisions, selection.approvalDecision);
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

function approvalDecisionLabel(
  decision: ApprovalDecision,
  t: ReturnType<typeof useTranslation>["t"],
): string {
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

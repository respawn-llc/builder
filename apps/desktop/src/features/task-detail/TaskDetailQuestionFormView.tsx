import {
  type MouseEvent,
  type ReactNode,
  type RefCallback,
  useCallback,
  useEffect,
  useId,
  useRef,
} from "react";
import { useTranslation } from "react-i18next";

import { errorMessage, type ApprovalDecision, type QuestionAttentionItem } from "@/api";
import type { QuestionAnswerInput } from "@/api";
import { useTextFieldSubmitShortcut } from "@/app-facade";
import { Button, RadioGroup, RadioGroupItem, showStatusToast, StaticMarkdown } from "@/ui";
import { cx, fieldInputClassName } from "@/ui";
import type { QuestionAnswerMutation } from "./TaskDetailQuestionAnswer";
import type { PromptPrimaryControl } from "./PromptPrimaryControlRegistry";
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
  registerPrimaryControl,
  selectionState,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  presentation: QuestionPresentation;
  registerPrimaryControl?: ((control: PromptPrimaryControl) => () => void) | undefined;
  selectionState: QuestionSelectionState;
}>) {
  const approvalDecisions =
    attention.question.kind === "approval" ? attention.question.approvalDecisions : null;
  if (approvalDecisions !== null) {
    return (
      <ApprovalQuestionForm
        answerQuestion={answerQuestion}
        approvalDecisions={approvalDecisions}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        question={presentation.question}
        registerPrimaryControl={registerPrimaryControl}
        selectionState={selectionState}
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
      registerPrimaryControl={registerPrimaryControl}
      selectionState={selectionState}
      suggestions={presentation.suggestions}
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
  registerPrimaryControl,
  selectionState,
  suggestions,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  recommendedOption: number | null;
  registerPrimaryControl?: ((control: PromptPrimaryControl) => () => void) | undefined;
  selectionState: QuestionSelectionState;
  suggestions: readonly string[];
}>) {
  const { t } = useTranslation();
  const selection = selectionState;
  const selectedOption = selection.selectedOption;
  const answer = selection.answer;
  const answerID = useId();
  const primaryControlRef = usePrimaryControlRef(registerPrimaryControl);
  // A real option can submit on its own; otherwise any typed freeform answer is
  // submittable, including freeform-only asks where no option is selected.
  const canSubmit = (selectedOption !== null && selectedOption > 0) || answer.trim().length > 0;
  const interactionDisabled = disabled || answerQuestion.isPending;
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
        promptID: attention.question.promptID,
        sessionID: attention.question.sessionID,
        stepID: attention.question.stepID,
        selectedOptionNumber: selectedOption,
        freeformAnswer: answer,
      }),
      selection,
    });
  }

  return (
    <QuestionFormFrame
      answer={answer}
      answerID={answerID}
      answerRef={suggestions.length === 0 ? primaryControlRef : undefined}
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
                primaryControlRef={optionIndex === 0 ? primaryControlRef : undefined}
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
      submitting={answerQuestion.isPending}
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
  registerPrimaryControl,
  selectionState,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  approvalDecisions: readonly ApprovalDecision[];
  attention: QuestionAttentionItem;
  disabled: boolean;
  onSelectionStateChange: (selection: QuestionSelectionState) => void;
  question: string | undefined;
  registerPrimaryControl?: ((control: PromptPrimaryControl) => () => void) | undefined;
  selectionState: QuestionSelectionState;
}>) {
  const { t } = useTranslation();
  const selection = selectionState;
  const selectedDecision = selectedApprovalDecisionFor(approvalDecisions, selection);
  const answer = selection.answer;
  const answerID = useId();
  const primaryControlRef = usePrimaryControlRef(registerPrimaryControl);
  const canSubmit = selectedDecision !== null && (selectedDecision !== "deny" || answer.trim().length > 0);
  const interactionDisabled = disabled || answerQuestion.isPending;

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
        promptID: attention.question.promptID,
        sessionID: attention.question.sessionID,
        stepID: attention.question.stepID,
        decision: selectedDecision,
        commentary: answer,
      }),
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
      optionGroup={approvalDecisions.map((decision, decisionIndex) => (
        <QuestionOption
          disabled={interactionDisabled}
          key={decision}
          primaryControlRef={decisionIndex === 0 ? primaryControlRef : undefined}
          recommended={false}
          text={approvalDecisionLabel(decision, t)}
          value={decision}
        />
      ))}
      question={question}
      radioValue={selectedDecision ?? ""}
      submitting={answerQuestion.isPending}
    />
  );
}

function QuestionFormFrame({
  answer,
  answerID,
  answerRef,
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
  answerRef?: RefCallback<HTMLTextAreaElement> | undefined;
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
        ref={answerRef}
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
  primaryControlRef,
  recommended,
  text,
  value,
}: Readonly<{
  disabled: boolean;
  primaryControlRef?: RefCallback<HTMLButtonElement> | undefined;
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
        ref={(element) => {
          radioRef.current = element;
          primaryControlRef?.(element);
        }}
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

function usePrimaryControlRef(
  register: ((control: PromptPrimaryControl) => () => void) | undefined,
): RefCallback<HTMLElement> {
  const unregisterRef = useRef<(() => void) | undefined>(undefined);
  useEffect(
    () => () => {
      unregisterRef.current?.();
      unregisterRef.current = undefined;
    },
    [],
  );
  return useCallback(
    (element) => {
      unregisterRef.current?.();
      unregisterRef.current = undefined;
      if (element !== null && register !== undefined) {
        unregisterRef.current = register({
          focusPrimary(options) {
            element.focus(options);
          },
        });
      }
    },
    [register],
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
  selection,
}: Readonly<{
  answerQuestion: QuestionAnswerMutation;
  attention: QuestionAttentionItem;
  failureTitle: string;
  input: () => QuestionAnswerInput;
  selection: QuestionSelectionState;
}>): Promise<void> {
  try {
    await answerQuestion.mutateAsync(input(), { attention, selection });
  } catch (error: unknown) {
    showStatusToast({
      body: errorMessage(error),
      id: `task-question-answer-failed:${attention.question.promptID}`,
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

import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { AttentionItem, TaskTransition } from "../../api";
import { errorMessage } from "../../api/errors";
import { useAppServices } from "../../app/useAppServices";
import { useOpenExternalLink } from "../../app/nativeHooks";
import { Button, Island, MarkdownText, RadioGroup, RadioGroupItem, showStatusToast } from "../../ui";
import { fieldInputClassName } from "../../ui/fieldInputStyles";
import { cx } from "../../ui/classes";
import { WorkflowEdgeRouteGraphic } from "../workflow-editor/WorkflowEdgeRouteGraphic";
import { emptyQuestionSelection, type QuestionSelectionState } from "./TaskDetailQuestionState";
import { usePendingAsks } from "./useTaskDetailData";
import type { useTaskMutations } from "./useTaskDetailData";

const emptySuggestions: readonly string[] = [];

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
  const asks = usePendingAsks(attention.sessionID);
  const pendingAsk = asks.data?.find((ask) => ask.askID === attention.askID);
  const question = attention.message.length > 0 ? attention.message : pendingAsk?.question;
  const suggestions = attention.suggestions.length > 0 ? attention.suggestions : pendingAsk?.suggestions ?? emptySuggestions;
  const recommendedOptionSource =
    attention.suggestions.length > 0 ? attention.recommendedOptionIndex : pendingAsk?.recommendedOptionIndex ?? 0;
  const recommendedOption = recommendedOptionNumber(suggestions, recommendedOptionSource);

  return (
    <Island aria-label={t("task.question")} className="p-[var(--space-4)]" level={1} radius="l" unpadded>
      <QuestionForm
        answerQuestion={mutations.answerQuestion}
        attention={attention}
        disabled={disabled}
        onSelectionStateChange={onSelectionStateChange}
        question={question}
        recommendedOption={recommendedOption}
        selectionState={selectionState}
        suggestions={suggestions}
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
  const { t } = useTranslation();
  const openLink = useOpenExternalLink();
  const selection = selectionForAsk(selectionState, attention.askID);
  const selectedOption = selection.userSelected ? selection.selectedOption : recommendedOption;
  const answer = selection.answer;
  const answerID = useId();
  // A real option (>0) can submit on its own; otherwise any typed freeform answer
  // is submittable, including freeform-only asks where no option is ever selected
  // (selectedOption stays null). submit() coerces a null/none selection to 0.
  const canSubmit = (selectedOption !== null && selectedOption > 0) || answer.trim().length > 0;
  const interactionDisabled = disabled || answerQuestion.isPending || selection.submitted;
  const submitDisabled = interactionDisabled || !canSubmit;
  const radioValue = selectedOption === null ? "" : selectedOption.toString();

  async function submit(): Promise<void> {
    const selectedOptionNumber = selectedOption ?? 0;
    await answerQuestion.mutateAsync({
      clientRequestID: `gui-question-${attention.askID}-${Date.now().toString()}`,
      taskID: taskId,
      runID: attention.runID,
      askID: attention.askID,
      selectedOptionNumber,
      freeformAnswer: answer,
    });
    onSelectionStateChange({ answer: "", askID: attention.askID, selectedOption: null, submitted: true, userSelected: true });
  }

  return (
    <form
      className="grid gap-[var(--space-2)]"
      onSubmit={(event) => {
        event.preventDefault();
        if (canSubmit && !interactionDisabled) {
          void submit();
        }
      }}
    >
      {question !== undefined && question.length > 0 ? (
        <div className="min-w-0 text-[var(--color-on-island)]">
          <MarkdownText onOpenLink={openLink} value={question} />
        </div>
      ) : null}
      <fieldset className="m-0 border-0 p-0">
        <legend className="sr-only">{t("task.optionNumber")}</legend>
        <RadioGroup
          aria-label={t("task.optionNumber")}
          disabled={interactionDisabled}
          onValueChange={(value) => {
            const nextOption = Number(value);
            onSelectionStateChange({
              answer,
              askID: attention.askID,
              selectedOption: nextOption,
              submitted: false,
              userSelected: true,
            });
          }}
          value={radioValue}
        >
          {suggestions.map((suggestion, optionIndex) => (
            <QuestionOption
              disabled={interactionDisabled}
              key={`${optionIndex.toString()}:${suggestion}`}
              onOpenLink={openLink}
              recommended={recommendedOption === optionIndex + 1}
              text={suggestion}
              value={(optionIndex + 1).toString()}
            />
          ))}
          <QuestionOption
            disabled={interactionDisabled}
            onOpenLink={openLink}
            recommended={false}
            text={t("task.neitherOption")}
            value="0"
          />
        </RadioGroup>
      </fieldset>
      <textarea
        aria-label={t("task.commentary")}
        className={cx(fieldInputClassName, "min-h-24")}
        disabled={interactionDisabled}
        id={answerID}
        onChange={(event) => {
          onSelectionStateChange({
            answer: event.target.value,
            askID: attention.askID,
            selectedOption,
            submitted: false,
            userSelected: selection.userSelected,
          });
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
        className={cx("min-w-0 flex-1 cursor-pointer", recommended && "font-bold text-[var(--color-primary)]")}
        htmlFor={id}
      >
        <MarkdownText inline onOpenLink={onOpenLink} value={text} />
        {recommended ? <span className="ml-[var(--space-2)] text-xs font-bold">({t("task.recommended")})</span> : null}
      </label>
    </div>
  );
}

function recommendedOptionNumber(suggestions: readonly string[], recommendedOptionIndex: number): number | null {
  return recommendedOptionIndex >= 1 && recommendedOptionIndex <= suggestions.length ? recommendedOptionIndex : null;
}

function selectionForAsk(selection: QuestionSelectionState, askID: string): QuestionSelectionState {
  return selection.askID === askID ? selection : emptyQuestionSelection(askID);
}

export function ApprovalBox({
  attention,
  currentVersion,
  disabled,
  mutations,
  transitions,
}: Readonly<{
  attention: AttentionItem;
  currentVersion: number;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
  transitions: readonly TaskTransition[];
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const transition = transitions.find((item) => item.id === attention.taskTransitionID);
  const stale = transition !== undefined && transition.version !== currentVersion;
  return (
    <Island
      aria-label={t("task.approval")}
      className="grid gap-[var(--space-2)] p-[var(--space-2)]"
      level={1}
      radius="l"
      unpadded
    >
      {transition !== undefined ? (
        <div className="grid gap-[var(--space-2)]">
          <div className="flex min-w-0 items-center gap-[var(--space-2)]" data-testid="task-approval-route-action-row">
            <WorkflowEdgeRouteGraphic
              className="-ml-[var(--space-2)]"
              contextMode=""
              layout="compact"
              neutralArrow
              sourceLabel={transition.sourceNodeName}
              targetLabel={transitionTargetLabel(transition, t)}
            />
            <span className="min-w-0 flex-1" />
            <Button
              className="shrink-0"
              disabled={disabled || mutations.approve.isPending}
              onClick={() => void mutations.approve.mutateAsync(attention.taskTransitionID)}
              variant="primary"
            >
              {t("task.approve")}
            </Button>
          </div>
          {transition.commentary.length > 0 ? (
            <p className="m-0 whitespace-pre-wrap text-sm text-[var(--color-muted)]">{transition.commentary}</p>
          ) : null}
          <ApprovalOutputValues
            nativeBridge={nativeBridge}
            outputValues={transition.outputValues}
            onCopied={(name) => {
              showStatusToast({
                id: `task-approval-output-copied-${name}`,
                title: t("task.outputValueCopied", { name }),
                tone: "success",
              });
            }}
            onCopyFailed={(name, error) => {
              showStatusToast({
                body: errorMessage(error),
                id: `task-approval-output-copy-failed-${name}`,
                title: t("task.outputValueCopyFailed", { name }),
                tone: "danger",
              });
            }}
          />
          {stale ? (
            <p className="m-0 text-sm text-[var(--color-warning)]">
              <strong>{t("task.staleApproval")}</strong> {t("task.staleApprovalBody")}
            </p>
          ) : null}
        </div>
      ) : (
        <>
          <p>{t("task.unavailableSnapshot")}</p>
          <Button
            disabled={disabled || mutations.approve.isPending}
            onClick={() => void mutations.approve.mutateAsync(attention.taskTransitionID)}
            variant="primary"
          >
            {t("task.approve")}
          </Button>
        </>
      )}
    </Island>
  );
}

export function InterruptedRunBox({
  attention,
  disabled,
  mutations,
}: Readonly<{
  attention: AttentionItem;
  disabled: boolean;
  mutations: ReturnType<typeof useTaskMutations>;
}>) {
  const { t } = useTranslation();
  return (
    <Island
      aria-label={t("task.interrupted")}
      className="grid gap-[var(--space-2)] p-[var(--space-4)]"
      level={1}
      radius="l"
      unpadded
    >
      <strong>{t("task.interrupted")}</strong>
      {attention.message.length > 0 ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{attention.message}</p>
      ) : null}
      <Button
        disabled={disabled || mutations.resume.isPending}
        onClick={() => void mutations.resume.mutateAsync()}
        variant="primary"
      >
        {t("board.resume")}
      </Button>
    </Island>
  );
}

function ApprovalOutputValues({
  nativeBridge,
  onCopyFailed,
  onCopied,
  outputValues,
}: Readonly<{
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"];
  onCopyFailed: (name: string, error: unknown) => void;
  onCopied: (name: string) => void;
  outputValues: Readonly<Record<string, string>>;
}>) {
  const { t } = useTranslation();
  const entries = Object.entries(outputValues);
  if (entries.length === 0) {
    return <p className="m-0 text-sm text-[var(--color-muted)]">{t("app.none")}</p>;
  }
  return (
    <div className="grid gap-[var(--space-2)]">
      {entries.map(([name, value], index) => (
        <div className="grid gap-[var(--space-2)]" key={name}>
          <div className="grid gap-[var(--space-1)]">
            <strong className="text-sm">{name}</strong>
            <button
              className="-mx-[var(--space-1)] min-w-0 whitespace-pre-wrap rounded-[var(--radius-m)] px-[var(--space-1)] py-[var(--space-1)] text-left text-sm text-[var(--color-muted)] transition-colors hover:bg-[var(--color-island-2)] hover:text-[var(--color-on-island)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-primary)]"
              onClick={() => {
                void copyText(value, nativeBridge)
                  .then(() => {
                    onCopied(name);
                  })
                  .catch((error: unknown) => {
                    onCopyFailed(name, error);
                  });
              }}
              type="button"
            >
              {value}
            </button>
          </div>
          {index < entries.length - 1 ? (
            <div className="px-[var(--space-2)]">
              <div className="h-px w-full bg-[var(--color-outline)]" />
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function transitionTargetLabel(transition: TaskTransition, fallback: ReturnType<typeof useTranslation>["t"]): string {
  const labels = transition.edges.map((edge) => edge.targetNodeName.trim()).filter((label) => label.length > 0);
  return labels.join(", ") || fallback("task.targetUnavailable");
}

async function copyText(
  value: string,
  nativeBridge: ReturnType<typeof useAppServices>["nativeBridge"],
): Promise<void> {
  if (nativeBridge.capabilities.clipboard.writeText) {
    await nativeBridge.clipboard.writeText(value);
    return;
  }
  await navigator.clipboard.writeText(value);
}

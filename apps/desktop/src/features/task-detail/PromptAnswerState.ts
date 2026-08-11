import type { PromptIdentity, QuestionAttentionItem } from "@/api";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";

export type PromptAnswerKey = Readonly<{
  sessionID: PromptIdentity["sessionID"];
  stepID: PromptIdentity["stepID"];
  promptID: PromptIdentity["promptID"];
}>;

export function promptAnswerKey(source: PromptIdentity | QuestionAttentionItem): PromptAnswerKey {
  const identity = "question" in source ? source.question : source;
  return Object.freeze({
    sessionID: identity.sessionID,
    stepID: identity.stepID,
    promptID: identity.promptID,
  });
}

export function samePromptAnswerKey(left: PromptAnswerKey, right: PromptAnswerKey): boolean {
  return (
    left.sessionID === right.sessionID && left.stepID === right.stepID && left.promptID === right.promptID
  );
}

export class PromptAnswerKeyMap<Value> implements Iterable<readonly [PromptAnswerKey, Value]> {
  private constructor(
    private readonly values: ReadonlyMap<string, ReadonlyMap<string, ReadonlyMap<string, Value>>>,
  ) {}

  static empty<Value>(): PromptAnswerKeyMap<Value> {
    return new PromptAnswerKeyMap(new Map());
  }

  get size(): number {
    let size = 0;
    for (const steps of this.values.values()) {
      for (const prompts of steps.values()) {
        size += prompts.size;
      }
    }
    return size;
  }

  get(key: PromptAnswerKey): Value | undefined {
    return this.values.get(key.sessionID)?.get(key.stepID)?.get(key.promptID);
  }

  has(key: PromptAnswerKey): boolean {
    return this.values.get(key.sessionID)?.get(key.stepID)?.has(key.promptID) === true;
  }

  with(key: PromptAnswerKey, value: Value): PromptAnswerKeyMap<Value> {
    const sessions = new Map(this.values);
    const steps = new Map(sessions.get(key.sessionID));
    const prompts = new Map(steps.get(key.stepID));
    prompts.set(key.promptID, value);
    steps.set(key.stepID, prompts);
    sessions.set(key.sessionID, steps);
    return new PromptAnswerKeyMap(sessions);
  }

  without(key: PromptAnswerKey): PromptAnswerKeyMap<Value> {
    const existingSteps = this.values.get(key.sessionID);
    const existingPrompts = existingSteps?.get(key.stepID);
    if (existingPrompts?.has(key.promptID) !== true) {
      return this;
    }
    const sessions = new Map(this.values);
    const steps = new Map(existingSteps);
    const prompts = new Map(existingPrompts);
    prompts.delete(key.promptID);
    if (prompts.size === 0) {
      steps.delete(key.stepID);
    } else {
      steps.set(key.stepID, prompts);
    }
    if (steps.size === 0) {
      sessions.delete(key.sessionID);
    } else {
      sessions.set(key.sessionID, steps);
    }
    return new PromptAnswerKeyMap(sessions);
  }

  *[Symbol.iterator](): Iterator<readonly [PromptAnswerKey, Value]> {
    for (const [sessionID, steps] of this.values) {
      for (const [stepID, prompts] of steps) {
        for (const [promptID, value] of prompts) {
          yield [{ sessionID, stepID, promptID }, value] as const;
        }
      }
    }
  }
}

export type FrozenPromptAnswer = Readonly<{
  attention: QuestionAttentionItem;
  selection: QuestionSelectionState;
}>;

export class PromptAnswerState {
  private constructor(
    private readonly selections: PromptAnswerKeyMap<QuestionSelectionState>,
    private readonly submissions: PromptAnswerKeyMap<FrozenPromptAnswer>,
  ) {}

  static empty(): PromptAnswerState {
    return new PromptAnswerState(
      PromptAnswerKeyMap.empty<QuestionSelectionState>(),
      PromptAnswerKeyMap.empty<FrozenPromptAnswer>(),
    );
  }

  selection(key: PromptAnswerKey): QuestionSelectionState | undefined {
    return this.selections.get(key);
  }

  frozenSubmission(key: PromptAnswerKey): FrozenPromptAnswer | undefined {
    return this.submissions.get(key);
  }

  isMasked(key: PromptAnswerKey): boolean {
    return this.submissions.has(key);
  }

  withSelection(key: PromptAnswerKey, selection: QuestionSelectionState): PromptAnswerState {
    return new PromptAnswerState(this.selections.with(key, selection), this.submissions);
  }

  beginSubmission(key: PromptAnswerKey, attention: QuestionAttentionItem): PromptAnswerState {
    const selection = this.selections.get(key);
    if (selection === undefined) {
      throw new Error(
        `Cannot submit prompt without selection state: session=${key.sessionID} step=${key.stepID} prompt=${key.promptID}`,
      );
    }
    return new PromptAnswerState(
      this.selections,
      this.submissions.with(key, Object.freeze({ attention, selection })),
    );
  }

  restoreSubmission(key: PromptAnswerKey): PromptAnswerState {
    const frozen = this.submissions.get(key);
    if (frozen === undefined) {
      return this;
    }
    return new PromptAnswerState(this.selections.with(key, frozen.selection), this.submissions.without(key));
  }

  discardSubmission(key: PromptAnswerKey): PromptAnswerState {
    return new PromptAnswerState(this.selections.without(key), this.submissions.without(key));
  }

  reconcileProjection(attentionItems: readonly QuestionAttentionItem[]): PromptAnswerState {
    const present = PromptAnswerKeyMap.empty<true>();
    let presentKeys = present;
    for (const attention of attentionItems) {
      presentKeys = presentKeys.with(promptAnswerKey(attention), true);
    }
    let selections = this.selections;
    for (const [key] of selections) {
      if (!presentKeys.has(key) && !this.submissions.has(key)) {
        selections = selections.without(key);
      }
    }
    return selections === this.selections ? this : new PromptAnswerState(selections, this.submissions);
  }
}

export function emptyPromptAnswerState(): PromptAnswerState {
  return PromptAnswerState.empty();
}

export type { QuestionSelectionState } from "./TaskDetailQuestionState";

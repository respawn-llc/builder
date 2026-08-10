import type { QuestionAttentionItem } from "@/api";
import { promptAnswerKey, samePromptAnswerKey, type PromptAnswerState } from "./PromptAnswerState";
import type { QuestionSelectionState } from "./TaskDetailQuestionState";

export type PromptAnswerFailure = Readonly<{
  cause: unknown;
  kind: "delivery" | "reconciliation";
  promptKey: ReturnType<typeof promptAnswerKey>;
  taskID: string;
  taskShortID: string;
  taskTitle: string;
}>;

type PromptAnswerCoordinatorDependencies = Readonly<{
  invalidateAttention(): Promise<void>;
  isMounted(): boolean;
  notifyFailure(failure: PromptAnswerFailure): void;
  readAttention(): Promise<readonly QuestionAttentionItem[]>;
  task: Readonly<{ id: string; shortID: string; title: string }>;
  updateState(update: (state: PromptAnswerState) => PromptAnswerState): void;
}>;

export class PromptAnswerCoordinator {
  constructor(private readonly dependencies: PromptAnswerCoordinatorDependencies) {}

  async submit({
    attention,
    selection,
    send,
  }: Readonly<{
    attention: QuestionAttentionItem;
    selection: QuestionSelectionState;
    send(): Promise<unknown>;
  }>): Promise<void> {
    const key = promptAnswerKey(attention);
    this.dependencies.updateState((state) =>
      state.withSelection(key, selection).beginSubmission(key, attention),
    );

    let deliveryFailed = false;
    let deliveryFailure: unknown;
    try {
      await send();
    } catch (error: unknown) {
      deliveryFailed = true;
      deliveryFailure = error;
    }

    try {
      await this.dependencies.invalidateAttention();
      const freshAttention = await this.dependencies.readAttention();
      if (!this.dependencies.isMounted()) {
        if (deliveryFailed) {
          this.notify("delivery", deliveryFailure, key);
        }
        return;
      }
      if (freshAttention.some((item) => samePromptAnswerKey(promptAnswerKey(item), key))) {
        this.dependencies.updateState((state) => state.restoreSubmission(key));
        if (deliveryFailed) {
          this.notify("delivery", deliveryFailure, key);
        }
        return;
      }
      this.dependencies.updateState((state) => state.discardSubmission(key));
    } catch (error: unknown) {
      if (this.dependencies.isMounted()) {
        this.dependencies.updateState((state) => state.restoreSubmission(key));
      }
      this.notify("reconciliation", error, key);
    }
  }

  private notify(
    kind: PromptAnswerFailure["kind"],
    cause: unknown,
    promptKey: PromptAnswerFailure["promptKey"],
  ): void {
    this.dependencies.notifyFailure({
      cause,
      kind,
      promptKey,
      taskID: this.dependencies.task.id,
      taskShortID: this.dependencies.task.shortID,
      taskTitle: this.dependencies.task.title,
    });
  }
}

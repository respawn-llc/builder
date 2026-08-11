import { PromptAnswerKeyMap, type PromptAnswerKey } from "./PromptAnswerState";

export type PromptPrimaryControl = Readonly<{
  focusPrimary(options: FocusOptions): void;
}>;

export type PromptPrimaryFocusRequest = Readonly<{
  key: PromptAnswerKey;
  requestID: number;
}>;

export class PromptPrimaryControlRegistry {
  private controls = PromptAnswerKeyMap.empty<PromptPrimaryControl>();

  register(key: PromptAnswerKey, control: PromptPrimaryControl): () => void {
    this.controls = this.controls.with(key, control);
    return () => {
      if (this.controls.get(key) === control) {
        this.controls = this.controls.without(key);
      }
    };
  }

  focus(key: PromptAnswerKey): boolean {
    const control = this.controls.get(key);
    if (control === undefined) {
      return false;
    }
    control.focusPrimary({ preventScroll: true });
    return true;
  }
}

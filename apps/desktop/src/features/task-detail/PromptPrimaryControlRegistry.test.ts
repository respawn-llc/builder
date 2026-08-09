import { describe, expect, it, vi } from "vitest";

import { PromptPrimaryControlRegistry } from "./PromptPrimaryControlRegistry";
import type { PromptAnswerKey } from "./PromptAnswerState";

describe("Task Detail prompt primary-control registry", () => {
  it("registers structurally by full key and unregisters only the exact mounted handle", () => {
    const registry = new PromptPrimaryControlRegistry();
    const key: PromptAnswerKey = {
      sessionID: "session-1",
      stepID: "step-1",
      promptID: "prompt-1",
    };
    const firstFocus = vi.fn();
    const secondFocus = vi.fn();
    const unregisterFirst = registry.register(key, { focusPrimary: firstFocus });
    const unregisterSecond = registry.register({ ...key }, { focusPrimary: secondFocus });

    unregisterFirst();
    expect(registry.focus({ ...key })).toBe(true);
    expect(secondFocus).toHaveBeenCalledWith({ preventScroll: true });
    expect(firstFocus).not.toHaveBeenCalled();

    unregisterSecond();
    expect(registry.focus(key)).toBe(false);
  });

  it("does not fall back to another prompt when the requested key is absent", () => {
    const registry = new PromptPrimaryControlRegistry();
    registry.register(
      { sessionID: "session-1", stepID: "step-1", promptID: "prompt-1" },
      { focusPrimary: vi.fn() },
    );

    expect(registry.focus({ sessionID: "session-1", stepID: "step-1", promptID: "prompt-2" })).toBe(false);
  });
});

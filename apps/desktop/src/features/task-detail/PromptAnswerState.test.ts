import { describe, expect, it } from "vitest";

import { promptAnswerKey, PromptAnswerKeyMap, samePromptAnswerKey } from "./PromptAnswerState";

describe("Task Detail prompt answer state", () => {
  it("isolates structurally equal and colliding Session, Step, and prompt keys", () => {
    const key = promptAnswerKey({ sessionID: "session-1", stepID: "step-1", promptID: "prompt-1" });
    const otherSession = { ...key, sessionID: "session-2" };
    const otherStep = { ...key, stepID: "step-2" };
    const otherPrompt = { ...key, promptID: "prompt-2" };
    const values = PromptAnswerKeyMap.empty<string>()
      .with(key, "base")
      .with(otherSession, "session")
      .with(otherStep, "step")
      .with(otherPrompt, "prompt");

    expect(Object.isFrozen(key)).toBe(true);
    expect(samePromptAnswerKey(key, { ...key })).toBe(true);
    expect([otherSession, otherStep, otherPrompt].every((other) => !samePromptAnswerKey(key, other))).toBe(
      true,
    );
    expect([...values].map(([, value]) => value)).toEqual(["base", "prompt", "step", "session"]);
    expect(values.without({ ...key }).get(key)).toBeUndefined();
    expect(values.get(otherSession)).toBe("session");
    expect(values.get(otherStep)).toBe("step");
    expect(values.get(otherPrompt)).toBe("prompt");
  });
});

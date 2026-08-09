import { describe, expect, it } from "vitest";

import { attentionItemSchema } from "@/api/schemas/common";
import { questionAttention } from "@/test-support/task-detail";
import { taskDetailAttentionRowKey } from "./TaskDetailAttentionRowKey";

describe("taskDetailAttentionRowKey", () => {
  it("preserves Questions with the same prompt ID from different Steps", () => {
    const first = attentionItemSchema.parse(questionAttention);
    const second = attentionItemSchema.parse({
      ...questionAttention,
      question: { ...questionAttention.question, step_id: "33333333-3333-4333-8333-333333333333" },
    });

    expect(taskDetailAttentionRowKey(first)).not.toBe(taskDetailAttentionRowKey(second));
  });
});

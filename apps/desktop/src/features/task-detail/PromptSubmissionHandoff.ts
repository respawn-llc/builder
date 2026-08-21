import type { AttentionItem } from "@/api";
import type { PromptPrimaryFocusRequest } from "./PromptPrimaryControlRegistry";
import { promptAnswerKey, samePromptAnswerKey, type PromptAnswerKey } from "./PromptAnswerState";

export type PromptSubmissionHandoff = Readonly<{
  primaryFocusRequest: PromptPrimaryFocusRequest | undefined;
}>;

export function promptSubmissionHandoff({
  attentionItems,
  requestID,
  submittedKey,
}: Readonly<{
  attentionItems: readonly AttentionItem[];
  requestID: number;
  submittedKey: PromptAnswerKey;
}>): PromptSubmissionHandoff {
  const submittedIndex = attentionItems.findIndex(
    (item) => item.kind === "question" && samePromptAnswerKey(promptAnswerKey(item), submittedKey),
  );
  const nextQuestion =
    submittedIndex < 0
      ? undefined
      : attentionItems
          .slice(submittedIndex + 1)
          .find((item): item is Extract<AttentionItem, { kind: "question" }> => item.kind === "question");
  return {
    primaryFocusRequest:
      nextQuestion === undefined
        ? undefined
        : {
            key: promptAnswerKey(nextQuestion),
            requestID,
          },
  };
}

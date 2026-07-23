import type { QuestionAnswerInput } from "@/api";

export type QuestionAnswerMutation = Readonly<{
  isPending: boolean;
  mutateAsync(input: QuestionAnswerInput): Promise<unknown>;
}>;

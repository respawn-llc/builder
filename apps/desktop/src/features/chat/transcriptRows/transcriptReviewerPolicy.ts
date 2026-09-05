export function reviewerFeedbackCopyText(suggestions: readonly string[]): string {
  return suggestions.map((suggestion, index) => `${String(index + 1)}. ${suggestion}`).join("\n");
}

import { describe, expect, it } from "vitest";

import { reviewerFeedbackCopyText } from "./transcriptReviewerPolicy";

describe("Chat Reviewer policy", () => {
  it("copies every suggestion in source order with independent boundaries", () => {
    expect(reviewerFeedbackCopyText(["**first**", "second\nwith detail"])).toBe(
      "1. **first**\n2. second\nwith detail",
    );
  });
});

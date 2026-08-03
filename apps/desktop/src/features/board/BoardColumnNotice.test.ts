import { describe, expect, it, vi } from "vitest";

import { boardColumnNoticeStatusNotice } from "./BoardColumnNotice";

describe("board column notices", () => {
  it("maps a retained-card failure to one actionable persistent notice", () => {
    const retry = vi.fn();
    const notice = boardColumnNoticeStatusNotice(
      {
        kind: "failure",
        columnID: "column-1",
        error: new Error("failed"),
        filter: {
          labelFilter: { kind: "none" },
          dependencyFilter: true,
        },
        generation: 3,
        noticeID: "notice-1",
        retry,
      },
      { cardsLoadFailed: "Cards failed", retry: "Retry" },
    );

    expect(notice).toMatchObject({
      actionLabel: "Retry",
      body: "failed",
      durationMs: Infinity,
      id: "notice-1",
      title: "Cards failed",
      tone: "danger",
    });
    notice?.onAction?.();
    expect(retry).toHaveBeenCalledOnce();
  });

  it("maps dismissal without creating a replacement notice", () => {
    expect(
      boardColumnNoticeStatusNotice(
        { kind: "dismiss", noticeID: "notice-1" },
        { cardsLoadFailed: "Cards failed", retry: "Retry" },
      ),
    ).toBeNull();
  });
});

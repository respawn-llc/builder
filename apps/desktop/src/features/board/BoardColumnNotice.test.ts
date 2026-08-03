import { describe, expect, it, vi } from "vitest";

import { boardColumnNoticeDiagnostic, boardColumnNoticeStatusNotice } from "./BoardColumnNotice";

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
      {
        cardsLoadRetryBody: "localized-retry-body",
        cardsLoadFailed: "localized-title",
        retry: "localized-retry",
      },
    );

    expect(notice).toMatchObject({
      actionLabel: "localized-retry",
      body: "localized-retry-body",
      durationMs: Infinity,
      id: "notice-1",
      title: "localized-title",
      tone: "danger",
    });
    notice?.onAction?.();
    expect(retry).toHaveBeenCalledOnce();
  });

  it("maps dismissal without creating a replacement notice", () => {
    expect(
      boardColumnNoticeStatusNotice(
        { kind: "dismiss", noticeID: "notice-1" },
        {
          cardsLoadRetryBody: "localized-retry-body",
          cardsLoadFailed: "localized-title",
          retry: "localized-retry",
        },
      ),
    ).toBeNull();
  });

  it("preserves the original failure in a structured diagnostic event", () => {
    const diagnostic = boardColumnNoticeDiagnostic(
      {
        kind: "failure",
        columnID: "column-1",
        error: new Error("diagnostic failure"),
        filter: {
          labelFilter: { kind: "none" },
          dependencyFilter: true,
        },
        generation: 3,
        noticeID: "notice-1",
        retry: vi.fn(),
      },
      { projectID: "project-1", workflowID: "workflow-1" },
    );

    expect(diagnostic?.context).toMatchObject({
      columnID: "column-1",
      error: "diagnostic failure",
      filterGeneration: "3",
      projectID: "project-1",
      workflowID: "workflow-1",
    });
  });
});

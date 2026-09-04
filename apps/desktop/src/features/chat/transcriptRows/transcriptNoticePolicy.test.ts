import { describe, expect, it } from "vitest";

import type { ChatTranscriptCommittedRow } from "@/api";

import { projectNotice } from "./transcriptNoticePolicy";

describe("Chat notice policy", () => {
  it("keeps notice inclusion, source, and expansion decisions typed", () => {
    const cases: readonly {
      name: string;
      row: ChatTranscriptCommittedRow;
      bodyKind: "markdown" | "plain_text" | null;
      defaultExpanded?: boolean;
    }[] = [
      { name: "hidden", row: noticeRow({ Visibility: "hidden" }), bodyKind: null },
      { name: "interruption", row: noticeRow({ MessageType: "interruption" }), bodyKind: null },
      {
        name: "reviewer status",
        row: noticeRow({ Diagnostic: { Code: "reviewer_status", Detail: "status" } }),
        bodyKind: null,
      },
      {
        name: "empty known context",
        row: noticeRow({ MessageType: "agents.md" }),
        bodyKind: null,
      },
      {
        name: "empty unknown context",
        row: noticeRow({
          Visibility: "detail",
          Reason: "runtime_diagnostic",
          MessageType: "future_context",
          Diagnostic: { Code: "future_context", Detail: "empty developer message" },
        }),
        bodyKind: "plain_text",
        defaultExpanded: true,
      },
      {
        name: "runtime diagnostic",
        row: noticeRow({
          Reason: "runtime_diagnostic",
          Diagnostic: { Code: "diagnostic", Detail: "diagnostic detail" },
        }),
        bodyKind: "plain_text",
        defaultExpanded: false,
      },
      {
        name: "Markdown context",
        row: noticeRow({
          MessageType: "agents.md",
          Diagnostic: { Code: "agents.md", Detail: "Markdown context" },
        }),
        bodyKind: "markdown",
        defaultExpanded: false,
      },
      {
        name: "warning",
        row: noticeRow({ Severity: "warning", LegacyText: "warning detail" }),
        bodyKind: "plain_text",
        defaultExpanded: true,
      },
    ];

    for (const testCase of cases) {
      const policy = projectNotice(testCase.row, prose);
      if (testCase.bodyKind === null) {
        expect(policy, testCase.name).toBeNull();
        continue;
      }
      expect(policy, testCase.name).not.toBeNull();
      if (policy === null) throw new Error(`Expected notice policy for ${testCase.name}.`);
      expect(policy.body.kind, testCase.name).toBe(testCase.bodyKind);
      expect(policy.defaultExpanded, testCase.name).toBe(testCase.defaultExpanded);
    }
  });

  it("renders compaction detail as Markdown and copies the complete detail", () => {
    const detail = "# Summary\n\n- preserve this";
    const policy = projectNotice(
      noticeRow({
        Reason: "compaction",
        MessageType: "compaction_summary",
        Compaction: { Count: 2, Detail: detail },
      }),
      { expanded: detail, compact: "compaction 2" },
    );
    expect(policy).not.toBeNull();
    if (policy === null) throw new Error("Expected compaction policy.");
    expect(policy.body).toEqual({ kind: "markdown", text: detail });
    expect(policy.copyText).toBe(detail);
    expect(policy.defaultExpanded).toBe(false);
  });
});

type Notice = NonNullable<ChatTranscriptCommittedRow["Notice"]>;

const prose = {
  expanded: "structured notice",
  compact: "localized notice",
};

function noticeRow(input: Partial<Notice> & { Visibility?: ChatTranscriptCommittedRow["Visibility"] } = {}) {
  const { Visibility = "ongoing", ...noticeInput } = input;
  return {
    Visibility,
    Integrity: 0,
    Kind: "notice",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: null,
    ReasoningTrace: null,
    Notice: {
      Reason: "legacy_untyped_notice",
      Severity: "info",
      ...noticeInput,
    },
    ReviewerFeedback: null,
    ReviewerError: null,
  } satisfies ChatTranscriptCommittedRow;
}

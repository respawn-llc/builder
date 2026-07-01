import { describe, expect, it } from "vitest";

import { workflowPromptTemplatePlaceholders } from "./workflowPromptTemplatePlaceholders";

describe("workflowPromptTemplatePlaceholders", () => {
  it("orders dynamic parameter placeholders before muted built-in placeholders", () => {
    expect(
      workflowPromptTemplatePlaceholders([
        { key: "summary" },
        { key: "INVALID" },
        { key: "notes" },
        { key: "summary" },
      ]),
    ).toEqual([
      { kind: "insert", label: ".Params.summary", tone: "primary", value: "{{.Params.summary}}" },
      { kind: "insert", label: ".Params.notes", tone: "primary", value: "{{.Params.notes}}" },
      { kind: "info", label: "{{.Params.<transition_key>.<parameter>}}", tone: "primary" },
      { kind: "insert", label: ".TaskId", tone: "muted", value: "{{.TaskId}}" },
      { kind: "insert", label: ".TaskShortId", tone: "muted", value: "{{.TaskShortId}}" },
      { kind: "insert", label: ".TaskTitle", tone: "muted", value: "{{.TaskTitle}}" },
      { kind: "insert", label: ".TaskBody", tone: "muted", value: "{{.TaskBody}}" },
      { kind: "insert", label: ".NodeId", tone: "muted", value: "{{.NodeId}}" },
      { kind: "insert", label: ".NodeKey", tone: "muted", value: "{{.NodeKey}}" },
      { kind: "insert", label: ".NodeDisplayName", tone: "muted", value: "{{.NodeDisplayName}}" },
      { kind: "insert", label: ".Params.commentary", tone: "muted", value: "{{.Params.commentary}}" },
    ]);
  });
});

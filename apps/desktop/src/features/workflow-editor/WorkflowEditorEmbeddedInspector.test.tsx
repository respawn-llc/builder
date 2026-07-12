import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { initializeI18n } from "../../i18n/setup";

interface InspectorProps {
  initialFocus: "firstEditableControl" | undefined;
  selection: unknown;
  workflowID: string;
}

const inspectorHarness = vi.hoisted(() => {
  const props: InspectorProps[] = [];
  return { props };
});

vi.mock("./WorkflowInspectorSidebar", () => ({
  WorkflowInspectorSidebar: ({
    initialFocus,
    selection,
    workflowID,
  }: Readonly<{
    initialFocus?: "firstEditableControl" | undefined;
    selection: unknown;
    workflowID: string;
  }>) => {
    inspectorHarness.props.push({ initialFocus, selection, workflowID });
    return <div data-testid="workflow-inspector-sidebar-boundary" />;
  },
}));

import { WorkflowEditorEmbeddedInspector } from "./WorkflowEditorEmbeddedInspector";

void initializeI18n();

describe("WorkflowEditorEmbeddedInspector", () => {
  beforeEach(() => {
    inspectorHarness.props.length = 0;
  });

  it("forwards keyboard initial-focus intent to the embedded inspector", () => {
    render(
      <WorkflowEditorEmbeddedInspector
        initialFocus="firstEditableControl"
        onClose={() => undefined}
        selection={{ edgeID: "edge-created", kind: "edge" }}
        workflowID="workflow-1"
      />,
    );

    expect(screen.getByTestId("workflow-inspector-sidebar-boundary")).toBeInTheDocument();
    expect(inspectorHarness.props).toEqual([
      {
        initialFocus: "firstEditableControl",
        selection: { edgeID: "edge-created", kind: "edge" },
        workflowID: "workflow-1",
      },
    ]);
  });

  it("does not introduce focus intent for pointer-origin inspection", () => {
    render(
      <WorkflowEditorEmbeddedInspector
        onClose={() => undefined}
        selection={{ edgeID: "edge-created", kind: "edge" }}
        workflowID="workflow-1"
      />,
    );

    expect(inspectorHarness.props[0]?.initialFocus).toBeUndefined();
  });
});

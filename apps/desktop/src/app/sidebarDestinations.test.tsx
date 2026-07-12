import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

interface InspectorProps {
  initialFocus: "firstEditableControl" | undefined;
  selection: unknown;
  workflowID: string;
}

const inspectorHarness = vi.hoisted(() => {
  const props: InspectorProps[] = [];
  return { props };
});

vi.mock("../features/workflow-editor/WorkflowInspectorSidebar", () => ({
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
    return <div data-testid="workflow-inspector-sidebar-destination" />;
  },
}));

import { SidebarDestinationView } from "./sidebarDestinations";

describe("SidebarDestinationView workflow inspector", () => {
  beforeEach(() => {
    inspectorHarness.props.length = 0;
  });

  it("forwards keyboard initial-focus intent to the overlay inspector destination", () => {
    render(
      <SidebarDestinationView
        closeSidebar={() => undefined}
        destination={{
          initialFocus: "firstEditableControl",
          kind: "workflowInspect",
          mode: "overlay",
          selection: { edgeID: "edge-created", kind: "edge" },
          workflowID: "workflow-1",
        }}
        resolveSidebar={() => undefined}
      />,
    );

    expect(screen.getByTestId("workflow-inspector-sidebar-destination")).toBeInTheDocument();
    expect(inspectorHarness.props).toEqual([
      {
        initialFocus: "firstEditableControl",
        selection: { edgeID: "edge-created", kind: "edge" },
        workflowID: "workflow-1",
      },
    ]);
  });

  it("keeps pointer-origin inspector destinations free of autofocus intent", () => {
    render(
      <SidebarDestinationView
        closeSidebar={() => undefined}
        destination={{
          kind: "workflowInspect",
          mode: "overlay",
          selection: { edgeID: "edge-created", kind: "edge" },
          workflowID: "workflow-1",
        }}
        resolveSidebar={() => undefined}
      />,
    );

    expect(inspectorHarness.props[0]?.initialFocus).toBeUndefined();
  });
});

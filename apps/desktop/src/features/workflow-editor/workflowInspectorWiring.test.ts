import { describe, expect, it } from "vitest";

import type { WorkflowDefinition, WorkflowEdge } from "../../api";
import {
  groupableWorkflowDefinition,
  workflowDefinition,
} from "../../test-support/workflow-editor/workflowEditorGraphMutationFixtures";
import {
  contextSourceFromSelectValue,
  contextSourceOptions,
  contextSourceSelectValue,
  immediateContextSourceOption,
  previousTargetContextSourceOption,
  previousTargetOrNewContextSourceOption,
  type Translate,
} from "./workflowInspectorWiring";

const translate = ((key: string) => {
  const values: Record<string, string> = {
    "workflowEditor.contextSourceImmediate": "Immediate source",
    "workflowEditor.contextSourceNode": "Node",
    "workflowEditor.contextSourcePreviousTarget": "Previous run of this target",
    "workflowEditor.contextSourcePreviousTargetOrNew": "Previous run of this target, or new session",
    "workflowEditor.contextSourceSelected": "Selected node",
    "workflowEditor.contextSourceUnavailable": "N/A for current configuration",
  };
  return values[key] ?? key;
}) as unknown as Translate;

describe("workflowInspectorWiring context sources", () => {
  it("keeps unavailable context-source options visible with disabled reasons", () => {
    const edge = edgeByID(groupableWorkflowDefinition, "edge-source-agent");

    const options = contextSourceOptions(groupableWorkflowDefinition, edge, translate);

    expect(optionByValue(options, previousTargetContextSourceOption)).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
    expect(optionByValue(options, previousTargetOrNewContextSourceOption)).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
    expect(optionByValue(options, "node-source")).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
    expect(optionByValue(options, "node-agent")).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
  });

  it("enables fallback previous target for non-dominating continuation edges", () => {
    const edge = {
      ...edgeByID(groupableWorkflowDefinition, "edge-source-agent"),
      contextMode: "continue_session",
      contextSource: { kind: "previous_target_or_new", nodeKey: "" },
    };

    const options = contextSourceOptions(groupableWorkflowDefinition, edge, translate);

    expect(optionByValue(options, previousTargetContextSourceOption)).toMatchObject({ disabled: true });
    expect(optionByValue(options, previousTargetOrNewContextSourceOption).disabled).toBeUndefined();
    expect(optionByValue(options, "node-source").disabled).toBeUndefined();
    expect(optionByValue(options, "node-agent")).toMatchObject({ disabled: true });
    expect(contextSourceSelectValue(groupableWorkflowDefinition, edge)).toBe(
      previousTargetOrNewContextSourceOption,
    );
    expect(
      contextSourceFromSelectValue(groupableWorkflowDefinition, previousTargetOrNewContextSourceOption),
    ).toEqual({
      kind: "previous_target_or_new",
      nodeKey: "",
    });
  });

  it("disables source-based options for invalid preserved start continuation edges", () => {
    const edge = {
      ...edgeByID(workflowDefinition, "edge-start"),
      contextMode: "continue_session",
    };

    const options = contextSourceOptions(workflowDefinition, edge, translate);

    expect(optionByValue(options, immediateContextSourceOption)).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
    expect(optionByValue(options, previousTargetOrNewContextSourceOption)).toMatchObject({
      disabled: true,
      disabledReason: "N/A for current configuration",
    });
  });
});

function edgeByID(definition: WorkflowDefinition, edgeID: string): WorkflowEdge {
  const edge = definition.edges.find((item) => item.id === edgeID);
  if (edge === undefined) {
    throw new Error(`missing edge ${edgeID}`);
  }
  return edge;
}

function optionByValue(options: ReturnType<typeof contextSourceOptions>, value: string) {
  const option = options.find((item) => item.value === value);
  if (option === undefined) {
    throw new Error(`missing option ${value}`);
  }
  return option;
}

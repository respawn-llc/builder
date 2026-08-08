import { describe, expect, it } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "@/api";
import { groupableWorkflowDefinition } from "./workflowEditorGraphMutationFixtures";
import { edgePromptPlaceholderParameters } from "./workflowInspectorWiring";

describe("edgePromptPlaceholderParameters", () => {
  it("omits protected parameters that the server marks as not materialized", () => {
    const sourceEdge = groupableWorkflowDefinition.edges.find((item) => item.id === "edge-source-agent");
    if (sourceEdge === undefined) {
      throw new Error("workflow fixture is missing the source-agent edge");
    }
    const edge = {
      ...sourceEdge,
      assigneeSelection: "previous_node" as const,
      parameters: [
        { key: "agent_role", description: "", purpose: "target_assignee" as const },
        { key: "summary", description: "Summary", purpose: "ordinary" as const },
      ],
    } satisfies WorkflowDefinition["edges"][number];
    const definition = {
      ...groupableWorkflowDefinition,
      derivedWiring: {
        ...emptyWorkflowDerivedWiring,
        edges: [
          {
            edgeID: edge.id,
            inputBindings: [],
            requiredProviderFields: [],
            requiredProvisionFields: [],
            assigneeSelectionApplicability: {
              available: true,
              parameterVisible: false,
              reason: "sole_callable_role" as const,
            },
            thinkingSelectionApplicability: {
              available: false,
              parameterVisible: false,
              reason: "unavailable_configuration" as const,
            },
          },
        ],
      },
      edges: groupableWorkflowDefinition.edges.map((item) => (item.id === edge.id ? edge : item)),
    };

    expect(edgePromptPlaceholderParameters(definition, edge)).toEqual([
      { key: "summary", description: "Summary", purpose: "ordinary" },
    ]);
  });
});

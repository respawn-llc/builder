import type { DraftWorkflowDefinition } from "./workflowEditorDraft";
import { connectWorkflowNodes } from "./workflowEditorGraphEdgeMutations";
import { addWorkflowNode } from "./workflowEditorGraphNodeMutations";
import type {
  AddConnectedWorkflowNodeInput,
  WorkflowEditorGraphMutationResult,
} from "./workflowEditorGraphMutationTypes";

export function addConnectedWorkflowNode(
  draft: DraftWorkflowDefinition,
  input: AddConnectedWorkflowNodeInput,
): WorkflowEditorGraphMutationResult {
  const addedNode = addWorkflowNode(draft, {
    id: input.nodeID,
    kind: input.kind,
  });
  const connected = connectWorkflowNodes(addedNode.draft, {
    edgeID: input.edgeID,
    sourceNodeID: input.sourceNodeID,
    targetNodeID: input.nodeID,
    transitionGroupID: input.transitionGroupID,
  });
  return connected.warnings.length === 0 ? connected : { ...connected, draft };
}

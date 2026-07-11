import type { WorkflowExecutionPolicy, WorkflowExecutionPolicyMode } from "../../api";

export function workflowExecutionPolicy(
  mode: WorkflowExecutionPolicyMode,
  customRef: string,
): WorkflowExecutionPolicy {
  if (mode === "custom_ref") {
    return { customRef, mode };
  }
  return { customRef: null, mode };
}

export function workflowExecutionPolicyModeFromSelectValue(value: string): WorkflowExecutionPolicyMode {
  switch (value) {
    case "none":
    case "head":
    case "default_branch":
    case "custom_ref":
    case "ask":
      return value;
    default:
      throw new Error(`Unknown workflow execution policy select value: ${value}`);
  }
}

import type { useTranslation } from "react-i18next";

import type { SidebarDestination } from "./sidebarContext";

export function sidebarTitle(
  destination: SidebarDestination,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (destination.kind === "newTask") {
    return t("task.newTitle");
  }
  if (destination.kind === "taskDetail") {
    return t("task.title");
  }
  if (destination.kind === "workflowCreate") {
    return t("workflowLibrary.createWorkflow");
  }
  if (destination.kind === "linkWorkflow") {
    return t("workflowLibrary.linkWorkflow");
  }
  if (destination.kind === "workflowInspect") {
    if (destination.selection.kind === "workflow") {
      return t("workflowEditor.inspectWorkflow");
    }
    if (destination.selection.kind === "node") {
      return t("workflowEditor.inspectNode");
    }
    if (destination.selection.kind === "group") {
      return t("workflowEditor.inspectGroup");
    }
    return t("workflowEditor.inspectEdge");
  }
  if (destination.kind === "workflowSettings") {
    return t("workflowEditor.editWorkflowTooltip");
  }
  if (destination.kind === "workflowEditor") {
    return t("workflowEditor.title");
  }
  return remainingSidebarTitle(destination, t);
}

function remainingSidebarTitle(
  destination: Extract<SidebarDestination, { kind: "projectEdit" | "processes" | "custom" }>,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  if (destination.kind === "projectEdit") return t("projectEdit.title");
  if (destination.kind === "processes") return t("processes.title");
  return destination.title;
}

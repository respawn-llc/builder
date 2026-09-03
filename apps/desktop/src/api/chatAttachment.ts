import { ContractError } from "./errors";
import type { ProjectAttachment, SessionAttachment } from "./transport";

export function requireProjectAttachment(
  attachment: ProjectAttachment | SessionAttachment | null,
  target: Readonly<{
    projectID: string;
    workspace: Readonly<{ workspaceID: string } | { workspaceRoot: string }>;
  }>,
): ProjectAttachment {
  if (attachment === null || "sessionID" in attachment) {
    throw new ContractError("Project attachment was not established.");
  }
  if (attachment.projectID !== target.projectID) {
    throw new ContractError("Project attachment does not match the requested Project.");
  }
  if ("workspaceID" in target.workspace) {
    if (
      attachment.workspaceSelection.kind !== "workspaceID" ||
      attachment.workspaceSelection.workspaceID !== target.workspace.workspaceID ||
      attachment.workspaceID !== target.workspace.workspaceID
    ) {
      throw new ContractError("Project attachment does not match the requested Workspace.");
    }
    return attachment;
  }
  if (
    attachment.workspaceSelection.kind !== "workspaceRoot" ||
    attachment.workspaceSelection.requestedRoot !== target.workspace.workspaceRoot ||
    attachment.workspaceSelection.canonicalRoot !== attachment.workspaceRoot
  ) {
    throw new ContractError("Project attachment does not match the requested Workspace.");
  }
  return attachment;
}

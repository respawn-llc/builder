import type { AppNavigation, LinkWorkflowCompletion } from "@/app-facade";

export async function completeBoardWorkflowLink(
  navigation: AppNavigation,
  projectID: string,
  completion: LinkWorkflowCompletion,
): Promise<void> {
  if (completion.kind === "created") {
    await navigation.openWorkflowEditor({ projectID, workflowID: completion.workflowID });
    return;
  }
  await navigation.openProject(projectID, completion.workflowID);
}

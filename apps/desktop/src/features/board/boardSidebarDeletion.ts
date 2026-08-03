import {
  sidebarDestinationIndexForTask,
  type SidebarDestination,
  type SidebarEntryToken,
} from "@/app-facade";

export function sidebarEntryTokenForDeletedTask(
  destinations: readonly SidebarDestination[],
  tokens: readonly SidebarEntryToken[],
  taskID: string,
): SidebarEntryToken | undefined {
  const index = sidebarDestinationIndexForTask(destinations, taskID);
  if (index !== undefined) {
    return tokens[index];
  }
  return undefined;
}

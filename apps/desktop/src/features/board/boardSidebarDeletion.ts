import {
  sidebarDestinationMatchesTask,
  type SidebarDestination,
  type SidebarEntryToken,
} from "@/app-facade";

export function sidebarEntryTokenForDeletedTask(
  destinations: readonly SidebarDestination[],
  tokens: readonly SidebarEntryToken[],
  taskID: string,
): SidebarEntryToken | undefined {
  for (const [index, destination] of destinations.entries()) {
    if (sidebarDestinationMatchesTask(destination, taskID)) {
      return tokens[index];
    }
  }
  return undefined;
}

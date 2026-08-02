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
  const index = destinations.findIndex((destination) =>
    sidebarDestinationMatchesTask(destination, taskID),
  );
  return index < 0 ? undefined : tokens[index];
}

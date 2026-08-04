import type { SidebarDestination, SidebarEntryToken } from "@/app-facade";

export function sidebarEntryTokenForDeletedTask(
  destinations: readonly SidebarDestination[],
  tokens: readonly SidebarEntryToken[],
  taskID: string,
): SidebarEntryToken | undefined {
  const index = destinations.findIndex(
    (destination) => destination.kind === "taskDetail" && destination.taskID === taskID,
  );
  return index < 0 ? undefined : tokens[index];
}

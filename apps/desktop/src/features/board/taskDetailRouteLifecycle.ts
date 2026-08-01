import type { SidebarResult } from "@/app-facade";

export function taskDetailRouteShouldClose(result: SidebarResult): boolean {
  return result.status === "submitted" || (result.status === "canceled" && result.reason === "closed");
}

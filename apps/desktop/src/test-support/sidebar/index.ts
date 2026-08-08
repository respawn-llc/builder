import type { SidebarController, SidebarDestination } from "@/app-facade";

export function createTestSidebarController(
  onOpen: (destination: SidebarDestination) => void = () => {
    return;
  },
): SidebarController {
  return {
    activeDestination: null,
    closeSidebar() {
      return;
    },
    completeCurrent() {
      return "stale";
    },
    async openSidebar(destination) {
      onOpen(destination);
      return { status: "canceled", reason: "closed" };
    },
    replaceSidebar(destination) {
      onOpen(destination);
    },
    phase: "open",
    resolveSidebar() {
      return;
    },
    resizeSidebar() {
      return;
    },
    sidebarWidthPx: 320,
  };
}

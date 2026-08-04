import type { SidebarController, SidebarDestination } from "@/app-facade";

export function createTestSidebarController(
  onOpen: (destination: SidebarDestination) => void = () => {
    return;
  },
): SidebarController {
  return {
    activeDestination: null,
    backSidebar() {
      return;
    },
    canGoBack: false,
    closeSidebar() {
      return;
    },
    invalidateSidebar() {
      return { kind: "absent" };
    },
    async openSidebar(destination) {
      onOpen(destination);
      return { status: "canceled", reason: "closed" };
    },
    pushSidebar(destination) {
      onOpen(destination);
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

import type { SidebarController, SidebarDestination } from "@/app-facade";

export function createTestSidebarController(
  onOpen: (destination: SidebarDestination) => void = () => {
    return;
  },
): SidebarController {
  return {
    activeDestination: null,
    activeSnapshot: null,
    activeToken: null,
    backSidebar() {
      return;
    },
    canGoBack: false,
    closeSidebar() {
      return;
    },
    closeSidebarIfCurrent() {
      return;
    },
    async openSidebar(destination) {
      onOpen(destination);
      return { status: "canceled", reason: "closed" };
    },
    pushSidebar(destination) {
      onOpen(destination);
    },
    preserveSidebarOnNextRouteChange() {
      return;
    },
    consumeSidebarRouteChangePreservation() {
      return false;
    },
    registerSidebarStateCapture() {
      return () => {
        return;
      };
    },
    removeSidebarEntry() {
      return;
    },
    replaceSidebar(destination) {
      onOpen(destination);
    },
    replaceSidebarIfCurrent(_token, destination) {
      onOpen(destination);
    },
    phase: "open",
    resolveSidebar() {
      return;
    },
    resolveSidebarIfCurrent() {
      return;
    },
    resizeSidebar() {
      return;
    },
    sidebarWidthPx: 320,
    stackDestinations: [],
    stackEntryTokens: [],
  };
}

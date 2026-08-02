import type { SidebarController, SidebarDestination } from "@/app-facade";

export function createTestSidebarController(
  onOpen: (destination: SidebarDestination) => void = () => {
    return;
  },
): SidebarController {
  return {
    activeDestination: null,
    activeActivationID: null,
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
    preserveSidebarOnNextRouteChange(token, expectation) {
      void token;
      void expectation;
      return;
    },
    clearSidebarRouteChangePreservation(token) {
      void token;
      return;
    },
    consumeSidebarRouteChangePreservation(location) {
      void location;
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

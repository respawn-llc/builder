import { createContext, useContext } from "react";

import type { SidebarDestination, SidebarPageNavigator } from "@/app-facade";
import type { SidebarStackEntry } from "./sidebarStack";

export interface SidebarCurrentPage {
  readonly Boundary: SidebarStackEntry["Boundary"];
  readonly destination: SidebarDestination;
  readonly navigator: SidebarPageNavigator;
  readonly retainedState?: unknown;
}

export const SidebarCurrentPageContext = createContext<SidebarCurrentPage | null>(null);

export function useSidebarCurrentPage(): SidebarCurrentPage | null {
  return useContext(SidebarCurrentPageContext);
}

import { createContext, useContext } from "react";

/**
 * Pixel height of the sidebar's overlay header. Sidebar destination content
 * renders behind the frosted header and offsets its first content past this
 * height. The default of 0 means surfaces rendered outside the sidebar (pop-out
 * windows, standalone routes) reserve no header padding.
 */
export const SidebarHeaderOffsetContext = createContext(0);

export function useSidebarHeaderOffset(): number {
  return useContext(SidebarHeaderOffsetContext);
}

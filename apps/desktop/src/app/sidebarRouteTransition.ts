export type SidebarRouteLocation = Readonly<{
  pathname: string;
  searchStr: string;
}>;
export type SidebarRouteTransition = "none" | "search" | "pathname";

export function classifySidebarRouteTransition(
  previousLocation: SidebarRouteLocation,
  nextLocation: SidebarRouteLocation,
): SidebarRouteTransition {
  if (previousLocation.pathname !== nextLocation.pathname) return "pathname";
  if (previousLocation.searchStr !== nextLocation.searchStr) return "search";
  return "none";
}

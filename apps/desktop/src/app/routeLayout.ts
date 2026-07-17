import { chromeContentPaddingClassName } from "@/ui";

const edgeToEdgeRoutePatterns = new Set(["/projects/$projectId", "/workflows/$workflowId/editor"]);

export function routeUsesEdgeToEdgeLayout(pathname: string): boolean {
  return edgeToEdgeRoutePatterns.has(routePattern(pathname));
}

export function routeFramePaddingClassName(pathname: string): string | undefined {
  if (routeUsesEdgeToEdgeLayout(pathname)) {
    return undefined;
  }
  // All island-paned routes share the thin chrome inset; the island pane owns the visible outer
  // padding. The home dashboard previously double-counted this with its own pane insets.
  return chromeContentPaddingClassName;
}

function routePattern(pathname: string): string {
  const segments = pathname.split("/").filter((segment) => segment.length > 0);
  if (segments.length === 2 && segments[0] === "projects") {
    return "/projects/$projectId";
  }
  if (segments.length === 3 && segments[0] === "workflows" && segments[2] === "editor") {
    return "/workflows/$workflowId/editor";
  }
  return pathname;
}

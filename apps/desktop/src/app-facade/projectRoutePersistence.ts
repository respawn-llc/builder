import { z } from "zod";

import { workflowIDSchema } from "@/api";
import { readBrowserStorage, removeBrowserStorage, writeBrowserStorage } from "./browserStorage";

const lastProjectRouteStorageKey = "desktop.lastProjectRoute";
export const projectContentTabs = ["tasks", "sessions", "subagents"] as const;
export type ProjectContentTab = (typeof projectContentTabs)[number];
const projectContentTabSchema = z.enum(projectContentTabs);
const storedProjectRouteSchema = z.discriminatedUnion("kind", [
  z.object({
    kind: z.literal("home_project"),
    projectId: z.string(),
    contentTab: projectContentTabSchema.optional(),
  }),
  z.object({
    kind: z.literal("workflow_board"),
    projectId: z.string(),
    workflowId: workflowIDSchema.optional(),
  }),
]);
type StoredProjectRoute = z.output<typeof storedProjectRouteSchema>;

export function readLastProjectRoute(): StoredProjectRoute | null {
  const stored = readBrowserStorage("local", lastProjectRouteStorageKey);
  if (!stored.ok) {
    return null;
  }
  const raw = stored.value ?? "null";
  try {
    const parsed: unknown = JSON.parse(raw);
    const route = storedProjectRouteSchema.safeParse(parsed);
    return route.success ? route.data : null;
  } catch {
    return null;
  }
}
export function writeLastProjectRoute(route: StoredProjectRoute): void {
  writeBrowserStorage("local", lastProjectRouteStorageKey, JSON.stringify(route));
}

export function writeLastProjectContentTab(projectID: string, contentTab: ProjectContentTab): void {
  writeLastProjectRoute({
    contentTab,
    kind: "home_project",
    projectId: projectID,
  });
}

export function clearLastProjectRoute(projectID: string): void {
  if (readLastProjectRoute()?.projectId === projectID) {
    removeBrowserStorage("local", lastProjectRouteStorageKey);
  }
}

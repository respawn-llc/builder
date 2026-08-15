import { z } from "zod";

import { workflowIDSchema } from "@/api";
import { readBrowserStorage, removeBrowserStorage, writeBrowserStorage } from "./browserStorage";

const lastProjectRouteStorageKey = "desktop.lastProjectRoute";
const storedProjectRouteSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("home_project"), projectId: z.string() }),
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
export function clearLastProjectRoute(projectID: string): void {
  if (readLastProjectRoute()?.projectId === projectID) {
    removeBrowserStorage("local", lastProjectRouteStorageKey);
  }
}

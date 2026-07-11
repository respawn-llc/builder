import { z } from "zod";

const lastProjectRouteStorageKey = "desktop.lastProjectRoute";
const storedProjectRouteSchema = z.object({
  projectId: z.string(),
  workflowId: z.string(),
});
type StoredProjectRoute = z.output<typeof storedProjectRouteSchema>;

export function readLastProjectRoute(): StoredProjectRoute | null {
  const raw = localStorageSafe()?.getItem(lastProjectRouteStorageKey) ?? "null";
  try {
    const parsed: unknown = JSON.parse(raw);
    const route = storedProjectRouteSchema.safeParse(parsed);
    return route.success ? route.data : null;
  } catch {
    return null;
  }
}
export function writeLastProjectRoute(route: StoredProjectRoute): void {
  localStorageSafe()?.setItem(lastProjectRouteStorageKey, JSON.stringify(route));
}
export function clearLastProjectRoute(projectID: string): void {
  const storage = localStorageSafe();
  if (storage !== null && readLastProjectRoute()?.projectId === projectID) {
    storage.removeItem(lastProjectRouteStorageKey);
  }
}
function localStorageSafe(): Storage | null {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

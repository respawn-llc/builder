import type { SidebarDestination } from "./sidebarContext";
import { defaultSidebarSizePreference, type SidebarSizePreference } from "./sidebarSizing";

export const taskDetailSidebarSizePreference: SidebarSizePreference = {
  desiredWidthPx: 650,
  minWidthPx: 520,
};

export type SidebarWidthProfile =
  | Readonly<{ kind: "custom"; sizing: SidebarSizePreference | null }>
  | Readonly<{ kind: "form" }>
  | Readonly<{ kind: "workflowList" }>
  | Readonly<{ kind: "projectEdit" }>
  | Readonly<{ kind: "taskDetail" }>
  | Readonly<{ kind: "workflowEditor" }>
  | Readonly<{ kind: "workflowInspector" }>;

export function sidebarWidthProfile(destination: SidebarDestination): SidebarWidthProfile {
  if (destination.kind === "taskDetail") {
    return { kind: "taskDetail" };
  }
  if (destination.kind === "workflowInspect" || destination.kind === "workflowSettings") {
    return { kind: "workflowInspector" };
  }
  if (destination.kind === "workflowEditor") {
    return { kind: "workflowEditor" };
  }
  if (destination.kind === "projectEdit") {
    return { kind: "projectEdit" };
  }
  if (
    destination.kind === "workflowCreate" ||
    destination.kind === "linkWorkflow" ||
    destination.kind === "processes"
  ) {
    return { kind: "workflowList" };
  }
  if (destination.kind === "custom") {
    return { kind: "custom", sizing: destination.sizing ?? null };
  }
  return { kind: "form" };
}

export function sidebarSizePreference(destination: SidebarDestination | null): SidebarSizePreference {
  if (destination === null) {
    return defaultSidebarSizePreference;
  }
  if (destination.kind === "custom") {
    return destination.sizing ?? defaultSidebarSizePreference;
  }
  return sidebarSizePreferenceForProfile(sidebarWidthProfile(destination));
}

function sidebarSizePreferenceForProfile(profile: SidebarWidthProfile): SidebarSizePreference {
  if (profile.kind === "custom") {
    return profile.sizing ?? defaultSidebarSizePreference;
  }
  if (profile.kind === "taskDetail") {
    return taskDetailSidebarSizePreference;
  }
  if (profile.kind === "workflowEditor") {
    return { desiredWidthPx: 550, minWidthPx: 420 };
  }
  if (profile.kind === "workflowInspector") {
    return { desiredWidthPx: 550, minWidthPx: 420 };
  }
  if (profile.kind === "projectEdit") {
    return { desiredWidthPx: 500, minWidthPx: 480 };
  }
  if (profile.kind === "workflowList") {
    return { desiredWidthPx: 500, minWidthPx: 420 };
  }
  return defaultSidebarSizePreference;
}

export function sidebarWidthProfileEquals(a: SidebarWidthProfile, b: SidebarWidthProfile): boolean {
  if (a.kind !== b.kind) {
    return false;
  }
  if (a.kind !== "custom" || b.kind !== "custom") {
    return true;
  }
  return sidebarSizePreferencesEqual(a.sizing, b.sizing);
}

function sidebarSizePreferencesEqual(
  a: SidebarSizePreference | null,
  b: SidebarSizePreference | null,
): boolean {
  if (a === null || b === null) {
    return a === b;
  }
  return a.desiredWidthPx === b.desiredWidthPx && a.minWidthPx === b.minWidthPx;
}

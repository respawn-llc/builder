import { createContext, useContext } from "react";

import type {
  SidebarCancelReason,
  SidebarDestination,
  SidebarDestinationSnapshot,
  SidebarResult,
  SidebarStateCapture,
} from "@/app-facade";

export type SidebarScopedActions = Readonly<{
  admitMutation(): (() => void) | null;
  capture(capture: SidebarStateCapture): () => void;
  close(reason?: SidebarCancelReason): void;
  invalidate(): void;
  replace(destination: SidebarDestination): void;
  resolve(result: Exclude<SidebarResult, { status: "canceled" }>): void;
}>;

export type SidebarHostState = Readonly<{
  actions: SidebarScopedActions;
  mutationAdmitted: boolean;
  direction: "push" | "back" | null;
  key: string | null;
  snapshot: SidebarDestinationSnapshot | null;
}>;

export const SidebarHostContext = createContext<SidebarHostState | null>(null);

export function useSidebarHost(): SidebarHostState {
  const value = useContext(SidebarHostContext);
  if (value === null) {
    throw new Error("SidebarHostContext is required.");
  }
  return value;
}

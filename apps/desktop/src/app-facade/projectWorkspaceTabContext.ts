import { createContext, useContext } from "react";

export type ProjectWorkspaceTab = "workflows" | "sessions";

export type ProjectWorkspaceTabContextValue = Readonly<{
  selectedTab: ProjectWorkspaceTab;
  selectTab(tab: ProjectWorkspaceTab): void;
}>;

export const ProjectWorkspaceTabContext =
  createContext<ProjectWorkspaceTabContextValue | null>(null);

export function useProjectWorkspaceTab(): ProjectWorkspaceTabContextValue {
  const value = useContext(ProjectWorkspaceTabContext);
  if (value === null) {
    throw new Error("ProjectWorkspaceTabProvider is required.");
  }
  return value;
}

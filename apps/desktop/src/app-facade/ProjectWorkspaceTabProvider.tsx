import { useMemo, useState, type ReactNode } from "react";

import {
  ProjectWorkspaceTabContext,
  type ProjectWorkspaceTab,
} from "./projectWorkspaceTabContext";

export function ProjectWorkspaceTabProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [selectedTab, selectTab] = useState<ProjectWorkspaceTab>("sessions");
  const value = useMemo(() => ({ selectedTab, selectTab }), [selectedTab]);
  return (
    <ProjectWorkspaceTabContext.Provider value={value}>
      {children}
    </ProjectWorkspaceTabContext.Provider>
  );
}

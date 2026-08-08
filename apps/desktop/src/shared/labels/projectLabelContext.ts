import { createContext, useContext } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import type { ProjectLabelCatalog } from "@/api";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import type { ProjectLabelFilterController } from "./projectLabelFilter";
import type { ProjectLabelEffects } from "./labelEventEffects";

export type ProjectLabelDataContextValue = Readonly<{
  authority: ProjectCatalogAuthority;
  catalog: UseQueryResult<ProjectLabelCatalog>;
  effects: ProjectLabelEffects;
  filter: ProjectLabelFilterController;
  projectID: string;
}>;

export const ProjectLabelDataContext = createContext<ProjectLabelDataContextValue | null>(null);

export function useProjectLabelData(): ProjectLabelDataContextValue {
  const value = useContext(ProjectLabelDataContext);
  if (value === null) {
    throw new Error("ProjectLabelsProvider is required");
  }
  return value;
}

import { useMutation } from "@tanstack/react-query";

import { useAppServices } from "@/app-facade";
import { useProjectLabelData } from "./projectLabelContext";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import type { ProjectLabelFilterController } from "./projectLabelFilter";
import type { ProjectLabelEffects } from "./labelEventEffects";

export function useProjectLabelCatalog() {
  return useProjectLabelData().catalog;
}

export function useProjectLabelCatalogMutations() {
  const { api } = useAppServices();
  const { effects, projectID } = useProjectLabelData();
  return {
    create: useMutation({
      mutationFn: async (name: string) => api.createProjectLabel(projectID, name),
      onSuccess: async (label) => effects.applyLocalCreate(label),
    }),
    rename: useMutation({
      mutationFn: async (input: Readonly<{ labelID: string; name: string }>) =>
        api.renameProjectLabel(projectID, input.labelID, input.name),
      onSuccess: async (label) => effects.applyLocalRename(label),
    }),
    delete: useMutation({
      mutationFn: async (labelID: string) => api.deleteProjectLabel(projectID, labelID),
      onSuccess: async (labelID) => effects.applyLocalDelete(labelID),
    }),
  };
}

export function useProjectCatalogAuthority(): ProjectCatalogAuthority {
  return useProjectLabelData().authority;
}

export function useProjectLabelFilter(): ProjectLabelFilterController {
  return useProjectLabelData().filter;
}

export function useProjectLabelEffects(): ProjectLabelEffects {
  return useProjectLabelData().effects;
}

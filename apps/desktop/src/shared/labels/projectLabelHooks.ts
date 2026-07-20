import { useMutation } from "@tanstack/react-query";

import { useAppServices } from "@/app-facade";
import { useProjectLabelData } from "./projectLabelContext";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import type { ProjectLabelFilterController } from "./projectLabelFilter";

export function useProjectLabelCatalog() {
  return useProjectLabelData().catalog;
}

export function useProjectLabelCatalogMutations() {
  const { api } = useAppServices();
  const { authority, filter, projectID } = useProjectLabelData();
  return {
    create: useMutation({
      mutationFn: async (name: string) => api.createProjectLabel(projectID, name),
      onSuccess: (label) => {
        authority.applyCreate(label);
      },
    }),
    rename: useMutation({
      mutationFn: async (input: Readonly<{ labelID: string; name: string }>) =>
        api.renameProjectLabel(projectID, input.labelID, input.name),
      onSuccess: (label) => {
        authority.applyRename(label);
      },
    }),
    delete: useMutation({
      mutationFn: async (labelID: string) => api.deleteProjectLabel(projectID, labelID),
      onSuccess: (labelID) => {
        authority.applyDelete(labelID);
        filter.dispatch({ type: "label.deleted", labelID });
      },
    }),
  };
}

export function useProjectCatalogAuthority(): ProjectCatalogAuthority {
  return useProjectLabelData().authority;
}

export function useProjectLabelFilter(): ProjectLabelFilterController {
  return useProjectLabelData().filter;
}

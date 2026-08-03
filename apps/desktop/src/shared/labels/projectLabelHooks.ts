import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { ProjectLabelCatalog } from "@/api";
import { queryKeys, useAppServices } from "@/app-facade";
import { useProjectLabelData } from "./projectLabelContext";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import type { ProjectLabelEffects } from "./labelEventEffects";
import type { ProjectLabelFilterController } from "./projectLabelFilter";

type ProjectLabelReorderContext = Readonly<{ generation: number; previous: ProjectLabelCatalog | undefined }>;

export function useProjectLabelCatalog() {
  return useProjectLabelData().catalog;
}

export function useProjectLabelCatalogMutations() {
  const { api } = useAppServices();
  const { authority, effects, projectID } = useProjectLabelData();
  const queryClient = useQueryClient();
  const queryKey = queryKeys.projectLabels(projectID);
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
    reorder: useMutation<ProjectLabelCatalog, unknown, readonly string[], ProjectLabelReorderContext>({
      mutationFn: async (labelIDs) => api.reorderProjectLabels(projectID, labelIDs),
      onMutate(labelIDs) {
        const generation = authority.supersedeReads();
        const previous = queryClient.getQueryData<ProjectLabelCatalog>(queryKey);
        if (previous === undefined) return { generation, previous };
        const labelsByID = new Map(previous.labels.map((label) => [label.id, label]));
        const labels = labelIDs.map((labelID) => {
          const label = labelsByID.get(labelID);
          if (label === undefined) {
            throw new Error(`Cannot reorder unknown Project label ${labelID} in Project ${projectID}.`);
          }
          labelsByID.delete(labelID);
          return label;
        });
        if (labels.length !== previous.labels.length || labelsByID.size !== 0) {
          throw new Error(`Project label reorder omitted a catalog label in Project ${projectID}.`);
        }
        queryClient.setQueryData(queryKey, { ...previous, labels });
        return { generation, previous };
      },
      onError(_error, _labelIDs, context) {
        if (context?.previous !== undefined) queryClient.setQueryData(queryKey, context.previous);
        authority.requestRefresh();
      },
      onSuccess(catalog, _labelIDs, context) { authority.installCatalog(catalog, context.generation); },
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

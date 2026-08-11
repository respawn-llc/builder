import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { ProjectLabel, ProjectLabelCatalog } from "@/api";
import { queryKeys, useAppServices } from "@/app-facade";
import { useProjectLabelData } from "./projectLabelContext";
import type { ProjectLabelEffects } from "./labelEventEffects";
import type { ProjectLabelFilterController } from "./projectLabelFilter";
import { pruneDeletedLabelFromExistingCaches } from "./taskLabelCache";

type ProjectLabelReorderContext = Readonly<{
  previous: ProjectLabelCatalog;
  projected: ProjectLabelCatalog;
}>;

export function useProjectLabelCatalog() {
  return useProjectLabelData().catalog;
}

export function useProjectLabelCatalogMutations() {
  const { api } = useAppServices();
  const { effects, filter, projectID } = useProjectLabelData();
  const queryClient = useQueryClient();
  const queryKey = queryKeys.projectLabels(projectID);
  const cancelCatalog = async (): Promise<void> => {
    await queryClient.cancelQueries({ queryKey, exact: true }, { revert: false, silent: true });
  };
  return {
    create: useMutation({
      mutationFn: async (name: string) => api.createProjectLabel(projectID, name),
      async onSuccess(label) {
        await cancelCatalog();
        patchCreatedLabel(queryClient, queryKey, label);
        effects.scheduleCatalogRefresh();
      },
    }),
    rename: useMutation({
      mutationFn: async (input: Readonly<{ labelID: string; name: string }>) =>
        api.renameProjectLabel(projectID, input.labelID, input.name),
      async onSuccess(label) {
        await cancelCatalog();
        patchRenamedLabel(queryClient, queryKey, label);
        effects.scheduleCatalogRefresh();
      },
    }),
    delete: useMutation({
      mutationFn: async (labelID: string) => api.deleteProjectLabel(projectID, labelID),
      async onSuccess(labelID) {
        await cancelCatalog();
        filter.dispatch({ type: "label.deleted", labelID });
        pruneDeletedLabelFromExistingCaches(queryClient, projectID, labelID);
        effects.scheduleDeleteRefresh();
      },
    }),
    reorder: useMutation<ProjectLabelCatalog, unknown, readonly string[], ProjectLabelReorderContext>({
      mutationFn: async (labelIDs) => api.reorderProjectLabels(projectID, labelIDs),
      async onMutate(labelIDs) {
        await cancelCatalog();
        const previous = queryClient.getQueryData<ProjectLabelCatalog>(queryKey);
        if (previous === undefined) {
          throw new Error(
            `Cannot reorder Project labels before the catalog for Project ${projectID} is loaded.`,
          );
        }
        const projected = projectCatalogPermutation(previous, labelIDs);
        queryClient.setQueryData(queryKey, projected);
        return { previous, projected };
      },
      onError(_error, _labelIDs, context) {
        if (
          context !== undefined &&
          catalogsStructurallyEqual(
            queryClient.getQueryData<ProjectLabelCatalog>(queryKey),
            context.projected,
          )
        ) {
          queryClient.setQueryData(queryKey, context.previous);
        }
        effects.scheduleCatalogRefresh();
      },
      async onSuccess(catalog) {
        await cancelCatalog();
        if (catalog.projectID !== projectID) {
          throw new Error(`Project label reorder returned ${catalog.projectID} while serving ${projectID}.`);
        }
        queryClient.setQueryData(queryKey, catalog);
        effects.scheduleReorderRefresh();
      },
    }),
  };
}

export function useProjectLabelFilter(): ProjectLabelFilterController {
  return useProjectLabelData().filter;
}

export function useProjectLabelEffects(): ProjectLabelEffects {
  return useProjectLabelData().effects;
}

function patchCreatedLabel(
  queryClient: ReturnType<typeof useQueryClient>,
  queryKey: ReturnType<typeof queryKeys.projectLabels>,
  label: ProjectLabel,
): void {
  queryClient.setQueryData<ProjectLabelCatalog>(queryKey, (catalog) =>
    catalog === undefined
      ? undefined
      : {
          ...catalog,
          labels: [label, ...catalog.labels.filter((candidate) => candidate.id !== label.id)],
        },
  );
}

function patchRenamedLabel(
  queryClient: ReturnType<typeof useQueryClient>,
  queryKey: ReturnType<typeof queryKeys.projectLabels>,
  label: ProjectLabel,
): void {
  queryClient.setQueryData<ProjectLabelCatalog>(queryKey, (catalog) =>
    catalog === undefined
      ? undefined
      : {
          ...catalog,
          labels: catalog.labels.map((candidate) => (candidate.id === label.id ? label : candidate)),
        },
  );
}

function projectCatalogPermutation(
  catalog: ProjectLabelCatalog,
  labelIDs: readonly string[],
): ProjectLabelCatalog {
  if (labelIDs.length !== catalog.labels.length) {
    throw new Error(
      `Project label reorder must contain every label in Project ${catalog.projectID} exactly once.`,
    );
  }
  const labelsByID = new Map(catalog.labels.map((label) => [label.id, label]));
  const labels = labelIDs.map((labelID) => {
    const label = labelsByID.get(labelID);
    if (label === undefined) {
      throw new Error(
        `Project label reorder contains an unknown or duplicate label ${labelID} in Project ${catalog.projectID}.`,
      );
    }
    labelsByID.delete(labelID);
    return label;
  });
  if (labelsByID.size !== 0) {
    throw new Error(
      `Project label reorder must contain every label in Project ${catalog.projectID} exactly once.`,
    );
  }
  return { ...catalog, labels };
}

function catalogsStructurallyEqual(
  left: ProjectLabelCatalog | undefined,
  right: ProjectLabelCatalog,
): boolean {
  return (
    left?.projectID === right.projectID &&
    left.labels.length === right.labels.length &&
    left.labels.every((label, index) => {
      const candidate = right.labels[index];
      return label.id === candidate?.id && label.name === candidate.name;
    })
  );
}

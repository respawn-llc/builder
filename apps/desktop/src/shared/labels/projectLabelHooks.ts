import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useCallback } from "react";
import type { Dispatch, KeyboardEvent, SetStateAction } from "react";

import { decodeWorkflowLabelError, errorMessage, type ProjectLabel, type ProjectLabelCatalog } from "@/api";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useProjectLabelData } from "./projectLabelContext";
import type { ProjectCatalogAuthority } from "./projectCatalogAuthority";
import type { ProjectLabelFilterController } from "./projectLabelFilter";
import type { ProjectLabelEffects } from "./labelEventEffects";
import type { LabelChooserInvocation } from "./LabelChooser";
import type {
  DeleteState,
  LabelFilterCondition,
  LabelResultRowSelection,
  RenameState,
} from "./LabelChooserRows";
import type { LabelFilterState } from "./labelFilterState";

export function useProjectLabelCatalog() {
  return useProjectLabelData().catalog;
}

export function useProjectLabelCatalogMutations() {
  const { api } = useAppServices();
  const { effects, projectID } = useProjectLabelData();
  const authority = useProjectCatalogAuthority();
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
    reorder: useMutation<ProjectLabelCatalog, unknown, readonly string[], ProjectLabelCatalog | undefined>({
      mutationFn: async (labelIDs) => api.reorderProjectLabels(projectID, labelIDs),
      onMutate(labelIDs) {
        const previous = queryClient.getQueryData<ProjectLabelCatalog>(queryKey);
        authority.applyReorder(labelIDs);
        return previous;
      },
      onError(_error, _labelIDs, previous) {
        if (previous !== undefined) {
          authority.restoreCatalog(previous);
        }
        void authority.requestRefresh();
      },
      onSuccess(catalog) {
        authority.acceptCatalog(catalog);
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

export function useProjectLabelEffects(): ProjectLabelEffects {
  return useProjectLabelData().effects;
}

export function labelMutationErrorMessage(error: unknown, t: TFunction): string {
  const labelError = decodeWorkflowLabelError(error);
  if (labelError === null) {
    return errorMessage(error);
  }
  switch (labelError.reason) {
    case "invalid_name":
      return t("labels.invalidName");
    case "name_conflict":
      return t("labels.nameConflict");
    case "catalog_limit":
      return t("labels.catalogLimit");
    case "project_not_found":
      return t("labels.projectMissing");
    case "label_not_found":
      return t("labels.labelMissing");
    case "task_not_found":
    case "wrong_project":
    case "invalid_filter":
    case "invalid_mutation":
      return t("labels.mutationFailed");
  }
}

export function handleLabelChooserSearchKeyDown({
  canCreate,
  catalogMutationPending,
  catalogAtLimit,
  choices,
  createLabel,
  event,
  highlightedIndex,
  invocation,
  setHighlightedIndex,
}: Readonly<{
  canCreate: boolean;
  catalogMutationPending: boolean;
  catalogAtLimit: boolean;
  choices: readonly (Readonly<{ kind: "unlabeled" }> | Readonly<{ kind: "label"; label: ProjectLabel }>)[];
  createLabel(): Promise<void>;
  event: KeyboardEvent<HTMLInputElement>;
  highlightedIndex: number | null;
  invocation: LabelChooserInvocation;
  setHighlightedIndex(update: (current: number | null) => number): void;
}>): void {
  if (handleLabelChoiceNavigation(event, choices.length, setHighlightedIndex)) {
    return;
  }
  if (event.key !== "Enter") {
    return;
  }
  if (choices.length > 0) {
    event.preventDefault();
    activateLabelChoice(choices, Math.min(highlightedIndex ?? 0, choices.length - 1), invocation);
    return;
  }
  if (canCreate && !catalogAtLimit && !catalogMutationPending) {
    event.preventDefault();
    void createLabel();
  }
}

function activateLabelChoice(
  choices: readonly (Readonly<{ kind: "unlabeled" }> | Readonly<{ kind: "label"; label: ProjectLabel }>)[],
  index: number,
  invocation: LabelChooserInvocation,
): void {
  const choice = choices[index];
  if (choice === undefined) {
    return;
  }
  if (choice.kind === "unlabeled") {
    selectUnlabeled(invocation);
    return;
  }
  const selection = labelResultRowSelection(invocation, choice.label.id);
  selectLabel(invocation, choice.label.id, selection.kind === "binary" ? !selection.selected : true);
}

export function labelResultRowSelection(
  invocation: LabelChooserInvocation,
  labelID: string,
): LabelResultRowSelection {
  if (invocation.kind === "assignment") {
    return {
      kind: "binary",
      selected: invocation.selectedLabelIDs.includes(labelID),
    };
  }
  return {
    kind: "condition",
    state: labelFilterCondition(invocation.state, labelID),
  };
}

export function selectLabel(invocation: LabelChooserInvocation, labelID: string, selected: boolean): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "named.cycle", labelID });
    return;
  }
  invocation.onSelectionChange(labelID, selected);
}

export function selectUnlabeled(invocation: LabelChooserInvocation): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "unlabeled.toggle" });
  }
}

export function removeDeletedSelection(invocation: LabelChooserInvocation, labelID: string): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "label.deleted", labelID });
    return;
  }
  if (invocation.selectedLabelIDs.includes(labelID)) {
    invocation.onSelectionChange(labelID, false);
  }
}

function labelFilterCondition(state: LabelFilterState, labelID: string): LabelFilterCondition {
  if (state.filter.kind !== "named") {
    return "neutral";
  }
  if (state.filter.labelIDs.includes(labelID)) {
    return "included";
  }
  return state.filter.excludedLabelIDs.includes(labelID) ? "excluded" : "neutral";
}

function handleLabelChoiceNavigation(
  event: KeyboardEvent<HTMLInputElement>,
  choiceCount: number,
  setHighlightedIndex: (update: (current: number | null) => number) => void,
): boolean {
  if (choiceCount === 0) {
    return false;
  }
  if (event.key === "ArrowDown") {
    event.preventDefault();
    setHighlightedIndex((current) => (current === null ? 0 : (current + 1) % choiceCount));
    return true;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    setHighlightedIndex((current) =>
      current === null ? choiceCount - 1 : (current - 1 + choiceCount) % choiceCount,
    );
    return true;
  }
  return false;
}

type LabelChooserMutations = ReturnType<typeof useProjectLabelCatalogMutations>;

export function useLabelChooserMutationActions({
  deletion,
  invocation,
  mutations,
  preparedSearch,
  rename,
  setDeletion,
  setKeyboardHighlightedIndex,
  setRename,
  setSearch,
  t,
}: Readonly<{
  deletion: DeleteState | null;
  invocation: LabelChooserInvocation;
  mutations: LabelChooserMutations;
  preparedSearch: string;
  rename: RenameState | null;
  setDeletion: Dispatch<SetStateAction<DeleteState | null>>;
  setKeyboardHighlightedIndex: Dispatch<SetStateAction<number | null>>;
  setRename: Dispatch<SetStateAction<RenameState | null>>;
  setSearch: Dispatch<SetStateAction<string>>;
  t: TFunction;
}>): Readonly<{
  catalogMutationPending: boolean;
  commitRename(): Promise<void>;
  confirmDelete(): Promise<void>;
  createError: string | null;
  createLabel(): Promise<void>;
}> {
  const catalogMutationPending =
    mutations.create.isPending ||
    mutations.reorder.isPending ||
    rename?.pending === true ||
    deletion?.pending === true;
  const createLabel = useCallback(async () => {
    if (catalogMutationPending) {
      return;
    }
    try {
      const label = await mutations.create.mutateAsync(preparedSearch);
      if (invocation.kind === "assignment") {
        invocation.onLabelCreated?.(label.id);
        selectLabel(invocation, label.id, true);
      }
      setSearch("");
      setKeyboardHighlightedIndex(null);
      mutations.create.reset();
    } catch {
      // The mutation owns the visible error state.
    }
  }, [catalogMutationPending, invocation, mutations.create, preparedSearch, setKeyboardHighlightedIndex, setSearch]);
  const commitRename = useCallback(async () => {
    if (rename === null || rename.pending || catalogMutationPending) {
      return;
    }
    const current = rename;
    setRename({ ...current, error: null, pending: true });
    try {
      await mutations.rename.mutateAsync({
        labelID: current.labelID,
        name: current.draft,
      });
      setRename((latest) => (latest?.labelID === current.labelID ? null : latest));
    } catch (error) {
      setRename((latest) =>
        latest?.labelID === current.labelID
          ? { ...latest, error: labelMutationErrorMessage(error, t), pending: false }
          : latest,
      );
    }
  }, [catalogMutationPending, mutations.rename, rename, setRename, t]);
  const confirmDelete = useCallback(async () => {
    if (deletion === null || deletion.pending || catalogMutationPending) {
      return;
    }
    const current = deletion;
    setDeletion({ ...current, error: null, pending: true });
    try {
      await mutations.delete.mutateAsync(current.labelID);
      removeDeletedSelection(invocation, current.labelID);
      setDeletion((latest) => (latest?.labelID === current.labelID ? null : latest));
    } catch (error) {
      setDeletion((latest) =>
        latest?.labelID === current.labelID
          ? { ...latest, error: labelMutationErrorMessage(error, t), pending: false }
          : latest,
      );
    }
  }, [catalogMutationPending, deletion, invocation, mutations.delete, setDeletion, t]);
  return {
    catalogMutationPending,
    commitRename,
    confirmDelete,
    createError: mutations.create.isError ? labelMutationErrorMessage(mutations.create.error, t) : null,
    createLabel,
  };
}

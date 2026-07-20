import { useMemo, useRef, useState, type KeyboardEvent, type ReactElement } from "react";
import { useTranslation } from "react-i18next";

import { decodeWorkflowLabelError, errorMessage } from "@/api";
import {
  Button,
  InteractiveChip,
  Popover,
  PopoverContent,
  PopoverTrigger,
  SegmentedControl,
  Spinner,
  TextInput,
} from "@/ui";
import {
  CreateLabelRow,
  LabelDeleteConfirmation,
  LabelRenameEditor,
  LabelResultRow,
  type DeleteState,
  type RenameState,
} from "./LabelChooserRows";
import { labelNameContains, labelNamesEqual } from "./labelComparison";
import type { LabelFilterAction, LabelFilterState } from "./labelFilterState";
import { useProjectLabelCatalog, useProjectLabelCatalogMutations } from "./projectLabelHooks";

const maxProjectLabels = 100;

export type LabelChooserInvocation =
  | Readonly<{
      kind: "filter";
      state: LabelFilterState;
      onAction(action: LabelFilterAction): void;
    }>
  | Readonly<{
      kind: "assignment";
      selectedLabelIDs: readonly string[];
      onSelectionChange(labelID: string, selected: boolean): void;
    }>;

export type LabelChooserProps = Readonly<{
  invocation: LabelChooserInvocation;
  trigger: ReactElement;
}>;

export function LabelChooser({ invocation, trigger }: LabelChooserProps) {
  const { t } = useTranslation();
  const catalog = useProjectLabelCatalog();
  const mutations = useProjectLabelCatalogMutations();
  const [search, setSearch] = useState("");
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [open, setOpen] = useState(false);
  const [rename, setRename] = useState<RenameState | null>(null);
  const [deletion, setDeletion] = useState<DeleteState | null>(null);
  const outsideInteractionRef = useRef(false);
  const mutationErrorMessage = (error: unknown): string => {
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
  };
  const preparedSearch = search.trim().normalize("NFC");
  const labels = useMemo(
    () => catalog.data?.labels.filter((label) => labelNameContains(label.name, preparedSearch)) ?? [],
    [catalog.data, preparedSearch],
  );
  const canCreate =
    preparedSearch.length > 0 && !labels.some((label) => labelNamesEqual(label.name, preparedSearch));
  const catalogAtLimit = (catalog.data?.labels.length ?? 0) >= maxProjectLabels;
  const choiceCount = labels.length + (canCreate ? 1 : 0);

  const createAndSelect = async () => {
    try {
      const label = await mutations.create.mutateAsync(preparedSearch);
      selectLabel(invocation, label.id, true);
    } catch {
      // The mutation owns the visible error state.
    }
  };
  const activateChoice = (index: number) => {
    const label = labels[index];
    if (label !== undefined) {
      const selected = isLabelSelected(invocation, label.id);
      selectLabel(invocation, label.id, !selected);
      return;
    }
    if (canCreate && !catalogAtLimit && index === labels.length) {
      void createAndSelect();
    }
  };
  const handleSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown" && choiceCount > 0) {
      event.preventDefault();
      setHighlightedIndex((current) => (current + 1) % choiceCount);
      return;
    }
    if (event.key === "ArrowUp" && choiceCount > 0) {
      event.preventDefault();
      setHighlightedIndex((current) => (current - 1 + choiceCount) % choiceCount);
      return;
    }
    if (event.key === "Enter" && choiceCount > 0) {
      event.preventDefault();
      activateChoice(Math.min(highlightedIndex, choiceCount - 1));
    }
  };
  const commitRename = async () => {
    if (rename === null || rename.pending) {
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
          ? {
              ...latest,
              error: mutationErrorMessage(error),
              pending: false,
            }
          : latest,
      );
    }
  };
  const confirmDelete = async () => {
    if (deletion === null || deletion.pending) {
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
          ? {
              ...latest,
              error: mutationErrorMessage(error),
              pending: false,
            }
          : latest,
      );
    }
  };
  return (
    <Popover
      onOpenChange={(nextOpen) => {
        if (!nextOpen && (rename !== null || deletion !== null) && !outsideInteractionRef.current) {
          setRename(null);
          setDeletion(null);
          return;
        }
        outsideInteractionRef.current = false;
        setOpen(nextOpen);
        if (!nextOpen) {
          setSearch("");
          setHighlightedIndex(0);
          setRename(null);
          setDeletion(null);
          mutations.create.reset();
          mutations.delete.reset();
          mutations.rename.reset();
        }
      }}
      open={open}
    >
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[min(22rem,calc(100vw-24px))] gap-[var(--space-2)] p-[var(--space-2)]"
        collisionPadding={12}
        level={3}
        onEscapeKeyDown={(event) => {
          if (rename === null && deletion === null) {
            return;
          }
          event.preventDefault();
          setRename(null);
          setDeletion(null);
        }}
        onPointerDownOutside={() => {
          outsideInteractionRef.current = true;
        }}
      >
        <TextInput
          autoComplete="off"
          error={mutations.create.isError ? mutationErrorMessage(mutations.create.error) : undefined}
          label={t("labels.search")}
          onChange={(event) => {
            setSearch(event.currentTarget.value);
            setHighlightedIndex(0);
            mutations.create.reset();
          }}
          onKeyDown={handleSearchKeyDown}
          value={search}
        />
        {invocation.kind === "filter" ? (
          <div className="flex items-center justify-between gap-[var(--space-2)]">
            <SegmentedControl
              ariaLabel={t("labels.matchMode")}
              disabled={invocation.state.filter.kind === "unlabeled"}
              onValueChange={(mode) => {
                invocation.onAction({ type: "named.mode", mode });
              }}
              options={[
                { label: t("labels.matchAny"), value: "any" },
                { label: t("labels.matchAll"), value: "all" },
              ]}
              value={invocation.state.namedMode}
            />
            <InteractiveChip
              onClick={() => {
                invocation.onAction({ type: "unlabeled.select" });
              }}
              selected={invocation.state.filter.kind === "unlabeled"}
              size="compact"
            >
              {t("labels.unlabeled")}
            </InteractiveChip>
          </div>
        ) : null}
        {catalog.isPending ? (
          <div className="grid min-h-20 place-items-center" role="status">
            <Spinner />
          </div>
        ) : catalog.isError ? (
          <div className="grid gap-[var(--space-2)] p-[var(--space-2)] text-sm text-[var(--color-error)]">
            <span>{t("labels.loadFailed")}</span>
            <Button
              onClick={() => {
                void catalog.refetch();
              }}
              variant="primary"
            >
              {t("app.retry")}
            </Button>
          </div>
        ) : (
          <div
            className="grid max-h-[min(calc(10*2.25rem+9*var(--space-1)),calc(var(--radix-popover-content-available-height)-10rem))] gap-[var(--space-1)] overflow-y-auto overscroll-contain pr-[var(--space-1)]"
            role="list"
          >
            {labels.map((label, index) => {
              if (deletion?.labelID === label.id) {
                return (
                  <LabelDeleteConfirmation
                    deletion={deletion}
                    key={label.id}
                    label={label}
                    onCancel={() => {
                      setDeletion(null);
                    }}
                    onConfirm={() => {
                      void confirmDelete();
                    }}
                  />
                );
              }
              if (rename?.labelID === label.id) {
                return (
                  <LabelRenameEditor
                    key={label.id}
                    onCancel={() => {
                      setRename(null);
                    }}
                    onChange={(draft) => {
                      setRename({ ...rename, draft, error: null });
                    }}
                    onCommit={() => {
                      void commitRename();
                    }}
                    rename={rename}
                  />
                );
              }
              const selected = isLabelSelected(invocation, label.id);
              return (
                <LabelResultRow
                  highlighted={index === highlightedIndex}
                  key={label.id}
                  label={label}
                  onDelete={() => {
                    setDeletion({
                      labelID: label.id,
                      error: null,
                      pending: false,
                    });
                  }}
                  onPointerEnter={() => {
                    setHighlightedIndex(index);
                  }}
                  onRename={() => {
                    setRename({
                      labelID: label.id,
                      draft: label.name,
                      error: null,
                      pending: false,
                    });
                  }}
                  onSelect={() => {
                    selectLabel(invocation, label.id, !selected);
                  }}
                  selected={selected}
                />
              );
            })}
            {canCreate ? (
              <CreateLabelRow
                atLimit={catalogAtLimit}
                highlighted={labels.length === highlightedIndex}
                name={preparedSearch}
                onPointerEnter={() => {
                  setHighlightedIndex(labels.length);
                }}
                onSelect={() => {
                  void createAndSelect();
                }}
                pending={mutations.create.isPending}
              />
            ) : null}
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

function isLabelSelected(invocation: LabelChooserInvocation, labelID: string): boolean {
  return invocation.kind === "filter"
    ? invocation.state.filter.kind === "named" && invocation.state.filter.labelIDs.includes(labelID)
    : invocation.selectedLabelIDs.includes(labelID);
}

function selectLabel(invocation: LabelChooserInvocation, labelID: string, selected: boolean): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "named.toggle", labelID });
    return;
  }
  invocation.onSelectionChange(labelID, selected);
}

function removeDeletedSelection(invocation: LabelChooserInvocation, labelID: string): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "label.deleted", labelID });
    return;
  }
  if (invocation.selectedLabelIDs.includes(labelID)) {
    invocation.onSelectionChange(labelID, false);
  }
}

import { Check, GripVertical, Pencil, Trash2, X } from "lucide-react";
import { useCallback, useId, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";

import type { ProjectLabel } from "@/api";
import type { ReorderableListItemRenderProps } from "@app/ui-kit";
import {
  ActionableListRow,
  Button,
  Chip,
  IconTooltipButton,
  Popover,
  PopoverContent,
  PopoverTrigger,
  fieldInputClassName,
} from "@/ui";

export type RenameState = Readonly<{
  labelID: string;
  draft: string;
  error: string | null;
  pending: boolean;
}>;

export type DeleteState = Readonly<{
  labelID: string;
  error: string | null;
  pending: boolean;
}>;

export type LabelFilterCondition = "neutral" | "included" | "excluded";

export type LabelResultRowSelection =
  | Readonly<{
      kind: "binary";
      selected: boolean;
    }>
  | Readonly<{
      kind: "condition";
      state: LabelFilterCondition;
    }>;

const conditionIndicatorVisibility = {
  neutral: "scale-75 opacity-0",
  included: "scale-100 opacity-100",
  excluded: "scale-100 opacity-100",
} as const;

export function LabelRenameEditor({
  catalogMutationPending,
  onCancel,
  onChange,
  onCommit,
  rename,
}: Readonly<{
  catalogMutationPending: boolean;
  onCancel(): void;
  onChange(draft: string): void;
  onCommit(): void;
  rename: RenameState;
}>) {
  const { t } = useTranslation();
  return (
    <form
      className="grid gap-[var(--space-1)] rounded-[var(--radius-s)] bg-[var(--color-island-1)] p-[var(--space-1)]"
      onSubmit={(event) => {
        event.preventDefault();
        onCommit();
      }}
      role="listitem"
    >
      <div className="flex min-w-0 items-center gap-[var(--space-1)]">
        <input
          aria-label={t("labels.renameField")}
          autoFocus
          className={`${fieldInputClassName} min-w-0 flex-1 py-[var(--space-1)]`}
          disabled={rename.pending || catalogMutationPending}
          onChange={(event) => {
            onChange(event.currentTarget.value);
          }}
          value={rename.draft}
        />
        <IconTooltipButton
          disabled={rename.pending || catalogMutationPending}
          label={t("labels.saveRename")}
          onClick={onCommit}
          size="icon-sm"
          variant="primary-outline"
        >
          <Check aria-hidden="true" size={14} strokeWidth={2} />
        </IconTooltipButton>
        <IconTooltipButton
          disabled={rename.pending}
          label={t("labels.cancelRename")}
          onClick={onCancel}
          size="icon-sm"
        >
          <X aria-hidden="true" size={14} strokeWidth={1.8} />
        </IconTooltipButton>
      </div>
      {rename.error === null ? null : (
        <span className="px-[var(--space-1)] text-xs text-[var(--color-error)]" role="alert">
          {rename.error}
        </span>
      )}
    </form>
  );
}

export function LabelResultRow({
  catalogMutationPending = false,
  deletion,
  highlighted,
  label,
  onDeleteConfirm,
  onDeleteOpenChange,
  onRename,
  onSelect,
  reorder,
  selection,
}: Readonly<{
  catalogMutationPending?: boolean;
  deletion: DeleteState | null;
  highlighted: boolean;
  label: ProjectLabel;
  onDeleteConfirm(): void;
  onDeleteOpenChange(open: boolean): void;
  onRename(): void;
  onSelect(): void;
  reorder?: ReorderableListItemRenderProps | undefined;
  selection: LabelResultRowSelection;
}>) {
  const { t } = useTranslation();
  const reorderActivatorRef = useCallback(
    (element: HTMLButtonElement | null) => {
      reorder?.activatorRef(element);
    },
    [reorder],
  );
  const reorderAttributes = reorder?.activatorAttributes;
  const reorderListeners = reorder?.activatorListeners;
  const deleteAction = (
    <Popover onOpenChange={onDeleteOpenChange} open={deletion !== null}>
      <PopoverTrigger asChild>
        <Button
          aria-label={t("labels.delete", { name: label.name })}
          disabled={catalogMutationPending}
          size="icon-sm"
          variant="ghost"
        >
          <Trash2 aria-hidden="true" className="text-[var(--color-error)]" size={14} strokeWidth={1.8} />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-56" level={4} side="top">
        <span className="text-sm text-[var(--color-muted)]">{t("labels.deleteBody")}</span>
        {deletion?.error === null || deletion === null ? null : (
          <span className="text-xs text-[var(--color-error)]" role="alert">
            {deletion.error}
          </span>
        )}
        <Button
          disabled={deletion?.pending === true || catalogMutationPending}
          onClick={onDeleteConfirm}
          variant="danger"
        >
          {t("app.confirm")}
        </Button>
      </PopoverContent>
    </Popover>
  );
  return (
    <LabelSelectionRow
      contextualActions={
        <div className="flex items-center gap-[var(--space-1)]">
          {deleteAction}
          <IconTooltipButton
            disabled={catalogMutationPending}
            label={t("labels.rename", { name: label.name })}
            onClick={onRename}
            size="icon-sm"
          >
            <Pencil aria-hidden="true" size={14} strokeWidth={1.8} />
          </IconTooltipButton>
        </div>
      }
      highlighted={highlighted}
      leadingActions={
        reorder === undefined ? undefined : (
          <Button
            aria-label={t("labels.reorder", { name: label.name })}
            className="text-[var(--color-muted)] hover:text-[var(--color-on-island)]"
            disabled={catalogMutationPending}
            ref={reorderActivatorRef}
            {...reorderAttributes}
            {...reorderListeners}
            size="icon-sm"
            variant="ghost"
          >
            <GripVertical aria-hidden="true" size={15} strokeWidth={1.8} />
          </Button>
        )
      }
      name={label.name}
      onSelect={onSelect}
      selection={selection}
    />
  );
}

export function UnlabeledResultRow({
  highlighted,
  name,
  onSelect,
  selected,
}: Readonly<{
  highlighted: boolean;
  name: string;
  onSelect(): void;
  selected: boolean;
}>) {
  return (
    <LabelSelectionRow
      highlighted={highlighted}
      name={name}
      onSelect={onSelect}
      selection={{ kind: "binary", selected }}
    />
  );
}

function LabelSelectionRow({
  contextualActions,
  highlighted,
  leadingActions,
  name,
  onSelect,
  selection,
}: Readonly<{
  contextualActions?: ReactNode;
  highlighted: boolean;
  leadingActions?: ReactNode;
  name: string;
  onSelect(): void;
  selection: LabelResultRowSelection;
}>) {
  const { t } = useTranslation();
  const conditionDescriptionID = useId();
  const presentation =
    selection.kind === "binary"
      ? {
          conditionDescription: null,
          conditionState: null,
          selectButtonProps: { onClick: onSelect },
          selected: selection.selected,
        }
      : {
          conditionDescription: labelConditionDescription(t, selection.state),
          conditionState: selection.state,
          selectButtonProps: {
            "aria-describedby": conditionDescriptionID,
            "aria-pressed": undefined,
            onClick: onSelect,
          },
          selected: selection.state !== "neutral",
        };
  return (
    <ActionableListRow
      className={highlighted ? "bg-[var(--color-island-1)]" : undefined}
      contextualActions={contextualActions}
      leadingActions={leadingActions}
      role="listitem"
      selectButtonProps={presentation.selectButtonProps}
      selected={presentation.selected}
    >
      <Chip className="min-w-[calc(3ch+var(--space-4))] justify-center" selected={presentation.selected}>
        <span className="min-w-0 truncate text-center">{name}</span>
      </Chip>
      {presentation.conditionDescription === null ? null : (
        <span className="sr-only" id={conditionDescriptionID}>
          {presentation.conditionDescription}
        </span>
      )}
      {presentation.conditionState === null ? (
        presentation.selected ? (
          <Check
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 right-[var(--space-2)] -translate-y-1/2 text-[var(--color-success)]"
            size={16}
            strokeWidth={1.8}
          />
        ) : null
      ) : (
        <span
          aria-hidden="true"
          className={`label-filter-condition-indicator pointer-events-none absolute top-1/2 right-[var(--space-2)] grid size-4 -translate-y-1/2 place-items-center ${
            conditionIndicatorVisibility[presentation.conditionState]
          }`}
        >
          {labelConditionIndicatorIcon(presentation.conditionState)}
        </span>
      )}
    </ActionableListRow>
  );
}

function labelConditionDescription(t: TFunction, state: LabelFilterCondition): string {
  switch (state) {
    case "neutral":
      return t("labels.filterConditionNeutral");
    case "included":
      return t("labels.filterConditionIncluded");
    case "excluded":
      return t("labels.filterConditionExcluded");
  }
}

function labelConditionIndicatorIcon(state: LabelFilterCondition): ReactNode {
  switch (state) {
    case "neutral":
      return null;
    case "included":
      return <Check className="text-[var(--color-success)]" size={16} strokeWidth={1.8} />;
    case "excluded":
      return <X className="text-[var(--color-error)]" size={16} strokeWidth={1.8} />;
  }
}

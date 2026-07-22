import { Check, Pencil, Trash2, X } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ProjectLabel } from "@/api";
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

export function LabelRenameEditor({
  onCancel,
  onChange,
  onCommit,
  rename,
}: Readonly<{
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
          disabled={rename.pending}
          onChange={(event) => {
            onChange(event.currentTarget.value);
          }}
          value={rename.draft}
        />
        <IconTooltipButton
          disabled={rename.pending}
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
  deletion,
  highlighted,
  label,
  onDeleteConfirm,
  onDeleteOpenChange,
  onRename,
  onSelect,
  selected,
}: Readonly<{
  deletion: DeleteState | null;
  highlighted: boolean;
  label: ProjectLabel;
  onDeleteConfirm(): void;
  onDeleteOpenChange(open: boolean): void;
  onRename(): void;
  onSelect(): void;
  selected: boolean;
}>) {
  const { t } = useTranslation();
  const deleteAction = (
    <Popover onOpenChange={onDeleteOpenChange} open={deletion !== null}>
      <PopoverTrigger asChild>
        <Button aria-label={t("labels.delete", { name: label.name })} size="icon-sm" variant="ghost">
          <Trash2
            aria-hidden="true"
            className="text-[var(--color-error)]"
            size={14}
            strokeWidth={1.8}
          />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-56" level={4} side="top">
        <span className="text-sm text-[var(--color-muted)]">{t("labels.deleteBody")}</span>
        {deletion?.error === null || deletion === null ? null : (
          <span className="text-xs text-[var(--color-error)]" role="alert">
            {deletion.error}
          </span>
        )}
        <Button disabled={deletion?.pending === true} onClick={onDeleteConfirm} variant="danger">
          {t("app.confirm")}
        </Button>
      </PopoverContent>
    </Popover>
  );
  return (
    <LabelSelectionRow
      contextualActions={
        <>
          {deleteAction}
          <IconTooltipButton label={t("labels.rename", { name: label.name })} onClick={onRename} size="icon-sm">
            <Pencil aria-hidden="true" size={14} strokeWidth={1.8} />
          </IconTooltipButton>
        </>
      }
      highlighted={highlighted}
      name={label.name}
      onSelect={onSelect}
      selected={selected}
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
      selected={selected}
    />
  );
}

function LabelSelectionRow({
  contextualActions,
  highlighted,
  name,
  onSelect,
  selected,
}: Readonly<{
  contextualActions?: ReactNode;
  highlighted: boolean;
  name: string;
  onSelect(): void;
  selected: boolean;
}>) {
  return (
    <ActionableListRow
      className={highlighted ? "bg-[var(--color-island-1)]" : undefined}
      contextualActions={contextualActions}
      role="listitem"
      selectButtonProps={{ onClick: onSelect }}
      selected={selected}
    >
      <Chip className="min-w-[calc(3ch+var(--space-4))] justify-center" selected={selected}>
        <span className="min-w-0 truncate text-center">{name}</span>
      </Chip>
      {selected ? (
        <Check
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 right-[var(--space-2)] -translate-y-1/2 text-[var(--color-success)]"
          size={16}
          strokeWidth={1.8}
        />
      ) : null}
    </ActionableListRow>
  );
}

import { Check, Pencil, Trash2, X } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { ProjectLabel } from "@/api";
import { ActionableListRow, Button, fieldInputClassName, IconTooltipButton } from "@/ui";

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

export function LabelDeleteConfirmation({
  deletion,
  label,
  onCancel,
  onConfirm,
}: Readonly<{
  deletion: DeleteState;
  label: ProjectLabel;
  onCancel(): void;
  onConfirm(): void;
}>) {
  const { t } = useTranslation();
  return (
    <div role="listitem">
      <section
        aria-label={t("labels.deleteConfirmation", { name: label.name })}
        className="grid gap-[var(--space-2)] rounded-[var(--radius-s)] border border-[color-mix(in_srgb,var(--color-error)_45%,transparent)] bg-[var(--color-island-1)] p-[var(--space-2)]"
        role="group"
      >
        <span className="text-sm font-bold">{label.name}</span>
        <span className="text-xs text-[var(--color-muted)]">{t("labels.deleteBody")}</span>
        {deletion.error === null ? null : (
          <span className="text-xs text-[var(--color-error)]" role="alert">
            {deletion.error}
          </span>
        )}
        <div className="flex justify-end gap-[var(--space-2)]">
          <Button disabled={deletion.pending} onClick={onCancel} variant="ghost">
            {t("app.cancel")}
          </Button>
          <Button disabled={deletion.pending} onClick={onConfirm} variant="danger">
            {t("labels.confirmDelete")}
          </Button>
        </div>
      </section>
    </div>
  );
}

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
  highlighted,
  label,
  onDelete,
  onPointerEnter,
  onRename,
  onSelect,
  selected,
}: Readonly<{
  highlighted: boolean;
  label: ProjectLabel;
  onDelete(): void;
  onPointerEnter(): void;
  onRename(): void;
  onSelect(): void;
  selected: boolean;
}>) {
  const { t } = useTranslation();
  return (
    <ActionableListRow
      actions={
        <IconTooltipButton label={t("labels.rename", { name: label.name })} onClick={onRename} size="icon-sm">
          <Pencil aria-hidden="true" size={14} strokeWidth={1.8} />
        </IconTooltipButton>
      }
      className={highlighted ? "bg-[var(--color-island-1)]" : undefined}
      contextualActions={
        <IconTooltipButton
          label={t("labels.delete", { name: label.name })}
          onClick={onDelete}
          size="icon-sm"
          variant="danger"
        >
          <Trash2 aria-hidden="true" size={14} strokeWidth={1.8} />
        </IconTooltipButton>
      }
      onPointerEnter={onPointerEnter}
      role="listitem"
      selected={selected}
      selectButtonProps={{ onClick: onSelect }}
    >
      <span className="flex min-w-0 items-center gap-[var(--space-2)]">
        <span className="min-w-0 flex-1 truncate">{label.name}</span>
        {selected ? (
          <Check
            aria-hidden="true"
            className="shrink-0 text-[var(--color-success)]"
            size={14}
            strokeWidth={2}
          />
        ) : null}
      </span>
    </ActionableListRow>
  );
}

export function CreateLabelRow({
  atLimit,
  highlighted,
  name,
  onPointerEnter,
  onSelect,
  pending,
}: Readonly<{
  atLimit: boolean;
  highlighted: boolean;
  name: string;
  onPointerEnter(): void;
  onSelect(): void;
  pending: boolean;
}>) {
  const { t } = useTranslation();
  const limitDescriptionID = useId();
  return (
    <ActionableListRow
      className={highlighted ? "bg-[var(--color-island-1)]" : undefined}
      onPointerEnter={onPointerEnter}
      role="listitem"
      selectButtonProps={{
        "aria-describedby": atLimit ? limitDescriptionID : undefined,
        "aria-label": t("labels.create", { name }),
        disabled: atLimit || pending,
        onClick: onSelect,
      }}
    >
      <span className="grid gap-[var(--space-1)]">
        <span>{t("labels.create", { name })}</span>
        {atLimit ? (
          <span className="text-xs text-[var(--color-muted)]" id={limitDescriptionID}>
            {t("labels.catalogLimit")}
          </span>
        ) : null}
      </span>
    </ActionableListRow>
  );
}

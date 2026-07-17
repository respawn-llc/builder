import type { SyntheticEvent } from "react";
import { useTranslation } from "react-i18next";

import { Button, Dialog, TextArea } from "@/ui";
import type { PendingMissingInputDrop } from "./BoardDropActions";

export function RollbackStartDialog({
  open,
  onClose,
  onConfirm,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog closeLabel={t("app.close")} onClose={onClose} open={open} title={t("board.rollbackStartTitle")}>
      <div className="grid gap-[var(--space-4)]">
        <p className="m-0 text-[var(--color-muted)]">{t("board.rollbackStartBody")}</p>
        <div className="flex justify-end gap-[var(--space-2)]">
          <Button onClick={onClose}>{t("app.cancel")}</Button>
          <Button onClick={onConfirm} variant="primary">
            {t("board.rollbackStartConfirm")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

export function MissingInputsDialog({
  drop,
  onClose,
  onSubmit,
  onValueChange,
}: Readonly<{
  drop: PendingMissingInputDrop | null;
  onClose: () => void;
  onSubmit: (event: SyntheticEvent<HTMLFormElement>) => void;
  onValueChange: (fieldName: string, value: string) => void;
}>) {
  const { t } = useTranslation();
  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onClose}
      open={drop !== null}
      title={t("board.missingInputsTitle")}
    >
      {drop === null ? null : (
        <form
          className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)_auto] gap-[var(--space-4)]"
          data-testid="missing-inputs-dialog-form"
          onSubmit={onSubmit}
        >
          <p className="m-0 text-[var(--color-muted)]">{t("board.missingInputsBody")}</p>
          <div
            className="grid min-h-0 gap-[var(--space-4)] overflow-auto pr-[var(--space-1)] hide-scrollbar"
            data-testid="missing-inputs-field-list"
          >
            {drop.fields.map((field) => (
              <TextArea
                className="!min-h-16"
                key={field.name}
                label={field.name}
                hint={field.description}
                onChange={(event) => {
                  onValueChange(field.name, event.currentTarget.value);
                }}
                required
                rows={2}
                value={drop.values[field.name] ?? ""}
              />
            ))}
          </div>
          <div className="flex justify-end gap-[var(--space-2)]" data-testid="missing-inputs-dialog-actions">
            <Button onClick={onClose}>{t("app.cancel")}</Button>
            <Button type="submit" variant="primary">
              {t("board.missingInputsConfirm")}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

import { useTranslation } from "react-i18next";

import { Button, Dialog } from "@/ui";

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

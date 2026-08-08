import { useState } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { Button, FloatingNoticeIsland } from "@/ui";

export function BoardBackgroundRefreshNotice({
  error,
  onRetry,
}: Readonly<{
  error: unknown;
  onRetry(): void;
}>) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);
  return (
    <FloatingNoticeIsland
      collapsed={collapsed}
      collapseLabel={t("app.collapse")}
      expandLabel={t("app.expand")}
      onCollapsedChange={setCollapsed}
      positionClassName="right-[var(--space-4)] top-[calc(var(--native-titlebar-height)+var(--space-4))]"
      title={t("board.loadFailed")}
      tone="danger"
    >
      <div className="grid gap-[var(--space-2)]">
        <p className="m-0 text-sm text-[var(--color-on-island)]">{errorMessage(error)}</p>
        <Button className="justify-self-start" onClick={onRetry} variant="primary">
          {t("app.retry")}
        </Button>
      </div>
    </FloatingNoticeIsland>
  );
}

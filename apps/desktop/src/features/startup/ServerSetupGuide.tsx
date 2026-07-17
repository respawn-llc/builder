import { type ReactElement } from "react";
import { useTranslation } from "react-i18next";
import { Server } from "lucide-react";

import { useOpenExternalLink } from "@/app-facade";
import { serverSetupDocsUrl } from "@/shared/product-links";
import { Button, EmptyState } from "@/ui";

export type ServerSetupGuideProps = Readonly<{
  detail: string;
  onCheckAgain: () => void;
}>;

export function ServerSetupGuide({ detail, onCheckAgain }: ServerSetupGuideProps): ReactElement {
  const { t } = useTranslation();
  const openExternalLink = useOpenExternalLink();

  return (
    <EmptyState
      actions={
        <>
          <Button
            data-testid="server-setup-open-docs"
            onClick={() => {
              openExternalLink(serverSetupDocsUrl);
            }}
            variant="primary"
          >
            {t("serverSetup.openDocs")}
          </Button>
          <Button data-testid="server-setup-check-again" onClick={onCheckAgain} variant="secondary">
            {t("serverSetup.checkAgain")}
          </Button>
        </>
      }
      body={t("serverSetup.body")}
      chromePadding
      icon={<Server size={28} strokeWidth={1.5} />}
      testID="server-setup-guide"
      title={t("serverSetup.title")}
    >
      {detail.length > 0 ? (
        <p className="m-0 max-w-[52ch] text-xs text-[var(--color-muted)]" data-testid="server-setup-detail">
          {t("serverSetup.detailLabel")}: {detail}
        </p>
      ) : null}
    </EmptyState>
  );
}

import type { ReactElement, ReactNode } from "react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate } from "@tanstack/react-router";

import { useStatusController } from "../../app/useStatusController";
import { useConnectionSnapshot } from "../../app/useConnectionSnapshot";
import { ErrorState, LoadingState } from "../../ui";
import { ServerSetupGuide } from "./ServerSetupGuide";
import { useStartup } from "./useStartup";

export type StartupGateProps = Readonly<{
  children: ReactNode;
}>;

export function StartupGate({ children }: StartupGateProps): ReactElement {
  const startup = useStartup();
  const connection = useConnectionSnapshot();
  const location = useLocation();
  const navigate = useNavigate();
  const { dismiss, push } = useStatusController();
  const { t } = useTranslation();
  const startupTitleKey = startup.kind === "error" ? startup.titleKey : "";

  useEffect(() => {
    if (connection.phase !== "disconnected") {
      dismiss("connection-lost");
      return;
    }
    push({
      id: "connection-lost",
      tone: "warning",
      title: t("app.reconnecting"),
      body: t("app.disconnected"),
      durationMs: Infinity,
    });
  }, [connection.phase, dismiss, push, t]);

  useEffect(() => {
    if (startupTitleKey !== "startup.updateTitle") {
      return;
    }
    if (location.pathname === "/") {
      return;
    }
    void navigate({ to: "/", replace: true });
  }, [location.pathname, navigate, startupTitleKey]);

  if (startup.kind === "loading") {
    return <LoadingState body={t("startup.loadingBody")} chromePadding reveal={false} title={t("startup.loadingTitle")} />;
  }

  if (startup.kind === "server-missing") {
    return <ServerSetupGuide detail={startup.detail} onCheckAgain={startup.retry} />;
  }

  if (startup.kind === "error") {
    return (
      <ErrorState
        body={startup.body}
        chromePadding
        onRetry={startup.retry}
        reveal={false}
        retryLabel={t("app.retry")}
        title={t(startup.titleKey)}
      />
    );
  }

  return <>{children}</>;
}

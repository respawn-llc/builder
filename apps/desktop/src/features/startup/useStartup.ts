import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { queryKeys } from "../../app/queryKeys";
import { useAppServices } from "../../app/useAppServices";
import type { ServerReadiness } from "../../api";
import { ProtocolMismatchError, TransportError, errorMessage } from "../../api/errors";
import { protocolVersion } from "../../api/jsonRpcSocket";

export type StartupViewModel =
  | Readonly<{ kind: "loading" }>
  | Readonly<{ kind: "ready"; readiness: ServerReadiness }>
  | Readonly<{ kind: "server-missing"; detail: string; retry(): void }>
  | Readonly<{ kind: "error"; titleKey: string; body: string; retry(): void }>;

export function useStartup(): StartupViewModel {
  const { api } = useAppServices();
  const { t } = useTranslation();
  const readiness = useQuery({
    queryKey: queryKeys.readiness,
    queryFn: async () => api.getReadiness(),
  });
  if (readiness.isPending) {
    return { kind: "loading" };
  }
  if (readiness.isError) {
    if (readiness.error instanceof ProtocolMismatchError) {
      return startupError(
        "startup.updateTitle",
        errorMessage(readiness.error),
        t("startup.unknownFailure"),
        () => {
          void readiness.refetch();
        },
      );
    }
    if (readiness.error instanceof TransportError) {
      // The loopback Kent server is unreachable — the desktop is a thin client and the
      // server is a separate install, so guide first-run setup instead of erroring.
      return {
        kind: "server-missing",
        detail: errorMessage(readiness.error),
        retry: () => {
          void readiness.refetch();
        },
      };
    }
    // The server is reachable but the readiness handshake failed (bad config, a
    // persistence-root mismatch, or an RPC/contract error). Surface it as a startup
    // error with retry rather than the first-run setup guide, which would be misleading
    // when the fix is to start or connect to the correct server.
    return startupError(
      "startup.errorTitle",
      errorMessage(readiness.error),
      t("startup.unknownFailure"),
      () => {
        void readiness.refetch();
      },
    );
  }
  if (!readiness.data.ready) {
    return startupError(
      "startup.errorTitle",
      readinessCause(readiness.data, t("startup.readinessFailed")),
      t("startup.unknownFailure"),
      () => {
        void readiness.refetch();
      },
    );
  }
  if (readiness.data.protocolVersion !== protocolVersion) {
    return startupError(
      "startup.updateTitle",
      protocolMismatchBody(
        t("startup.clientProtocol", { protocol: protocolVersion }),
        t("startup.serverProtocol", { protocol: readiness.data.protocolVersion }),
        t("startup.updateSameBuild"),
      ),
      t("startup.unknownFailure"),
      () => {
        void readiness.refetch();
      },
    );
  }
  return { kind: "ready", readiness: readiness.data };
}

function startupError(
  titleKey: string,
  body: string,
  fallbackBody: string,
  retry: () => void,
): StartupViewModel {
  return { kind: "error", titleKey, body: body.length > 0 ? body : fallbackBody, retry };
}

function readinessCause(readiness: ServerReadiness, fallbackBody: string): string {
  const cause = readiness.causes[0];
  if (cause === undefined) {
    return fallbackBody;
  }
  const body = [cause.summary, cause.nextAction]
    .filter((part): part is string => part !== undefined && part.length > 0)
    .join(" ");
  return body.length > 0 ? body : fallbackBody;
}

function protocolMismatchBody(
  clientProtocol: string,
  serverProtocol: string,
  updateInstruction: string,
): string {
  return [clientProtocol, serverProtocol, updateInstruction].join(" ");
}

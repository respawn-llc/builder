import { createBrowserNativeBridge } from "@app/native-bridge";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, useMemo, type ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import { ApiClient, protocolVersion } from "@/api/composition";
import {
  AppServicesProvider,
  StatusProvider,
  WindowChromeTitleProvider,
  type AppLogger,
  type AppLogLevel,
  type AppServices,
} from "@/app-facade";
import { appI18n, initializeI18n } from "@/i18n";
import { FakeRpcTransport, type FakeRoute } from "../api";

void initializeI18n();

export type TestLogEntry = Readonly<{
  context: Readonly<Record<string, string>>;
  level: AppLogLevel;
  message: string;
}>;

export type TestLogger = AppLogger &
  Readonly<{
    entries(): readonly TestLogEntry[];
  }>;

export const clientProtocolVersion = protocolVersion;

export type TestAppServices = Omit<AppServices, "logger"> &
  Readonly<{
    logger: TestLogger;
    transport: FakeRpcTransport;
  }>;

export type CreateTestServicesOptions = Readonly<{
  debugThemeOverrideEnabled?: boolean | undefined;
  homePath?: string | undefined;
}>;

export function TestAppProviders({
  children,
  services,
}: Readonly<{
  children: ReactNode;
  services: AppServices;
}>) {
  const queryClient = useMemo(
    () =>
      new QueryClient({
        defaultOptions: {
          mutations: { retry: false },
          queries: { retry: false },
        },
      }),
    [],
  );
  return createElement(
    I18nextProvider,
    { i18n: appI18n },
    createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(AppServicesProvider, {
        services,
        children: createElement(WindowChromeTitleProvider, {
          children: createElement(StatusProvider, { children }),
        }),
      }),
    ),
  );
}

export function createTestServices(
  routes: readonly FakeRoute[],
  nativeBridge = createBrowserNativeBridge(),
  options: CreateTestServicesOptions = {},
): TestAppServices {
  const transport = new FakeRpcTransport(routes);
  return {
    api: new ApiClient(transport),
    debugThemeOverrideEnabled: options.debugThemeOverrideEnabled ?? false,
    endpoint: "ws://127.0.0.1:53082/rpc",
    homePath: options.homePath ?? "",
    logger: createTestLogger(),
    nativeBridge,
    protocolVersion,
    storageNamespace: {
      kind: "browser-endpoint",
      identity: "ws://127.0.0.1:53082/rpc",
    },
    transport,
  };
}

function createTestLogger(): TestLogger {
  const entries: TestLogEntry[] = [];
  return {
    async append(level, message, context = {}) {
      entries.push({ context, level, message });
    },
    entries() {
      return entries.slice();
    },
  };
}

export const startupRoutes: readonly FakeRoute[] = [
  {
    method: "server.readiness.get",
    result: {
      ready: true,
      server_id: "server-1",
      server_version: "1.3.0",
      server_build: "1.3.0",
      protocol_version: protocolVersion,
      auth_ready: true,
      auth_required: true,
      endpoint: "ws://127.0.0.1:53082/rpc",
      subagent_roles: [{ name: "default" }, { name: "fast" }, { name: "coder" }, { name: "reviewer" }],
    },
  },
  {
    method: "project.home.list",
    result: {
      projects: [],
      next_page_token: "",
      generated_at_unix_ms: 1,
    },
  },
  {
    method: "workflow.attention.list",
    result: {
      items: [],
      next_page_token: "",
      generated_at_unix_ms: 1,
    },
  },
  {
    method: "workflow.task.comment.list",
    result: {
      comments: [],
      next_page_token: "",
    },
  },
];

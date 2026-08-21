import { createBrowserNativeBridge, type NativePlatform } from "@app/native-bridge";
import { create } from "@app/server-api-contract";
import {
  GetReadinessResultSchema,
  ServerService,
} from "@app/server-api-contract/gen/kent/api/server/server_pb";
import { ProjectCatalogService } from "@app/server-api-contract/gen/kent/api/project/project_pb";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, useMemo, type ReactNode } from "react";
import { I18nextProvider } from "react-i18next";

import { ApiClient, protocolVersion } from "@/api/composition";
import {
  AppServicesProvider,
  StatusProvider,
  TaskSearchMemoryProvider,
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
  platform?: NativePlatform | undefined;
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
          children: createElement(StatusProvider, {
            children: createElement(TaskSearchMemoryProvider, { children }),
          }),
        }),
      }),
    ),
  );
}

export function createTestServices(
  routes: readonly FakeRoute[],
  nativeBridge?: ReturnType<typeof createBrowserNativeBridge>,
  options: CreateTestServicesOptions = {},
): TestAppServices {
  const transport = new FakeRpcTransport(routes);
  const resolvedNativeBridge =
    nativeBridge ??
    createBrowserNativeBridge(options.platform === undefined ? {} : { platform: options.platform });
  return {
    api: new ApiClient(transport),
    debugThemeOverrideEnabled: options.debugThemeOverrideEnabled ?? false,
    endpoint: "ws://127.0.0.1:53082/rpc",
    homePath: options.homePath ?? "",
    logger: createTestLogger(),
    nativeBridge: resolvedNativeBridge,
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
    descriptor: ServerService.method.getReadiness,
    result: create(GetReadinessResultSchema, {
      outcome: {
        case: "success",
        value: {
          readiness: {
            ready: true,
            serverId: "server-1",
            serverVersion: "1.3.0",
            serverBuild: "1.3.0",
            protocolVersion,
            authReady: true,
            authRequired: true,
            endpoint: "ws://127.0.0.1:53082/rpc",
            subagentRoles: [{ name: "default" }, { name: "fast" }, { name: "coder" }, { name: "reviewer" }],
            causes: [],
          },
        },
      },
    }),
  },
  {
    descriptor: ProjectCatalogService.method.listHome,
    result: create(ProjectCatalogService.method.listHome.output, {
      outcome: {
        case: "success",
        value: {
          projects: [],
          generatedAt: { seconds: 1n, nanos: 0 },
        },
      },
    }),
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
      items: [],
      next_offset: null,
      total_count: 0,
    },
  },
  {
    method: "workflow.project.label.list",
    result: {
      catalog: {
        project_id: "project-1",
        labels: [],
      },
    },
  },
];

import { createAutoNativeBridge, type NativePlatform } from "@app/native-bridge";
import { z } from "zod";

import { ApiClient, createJsonRpcTransport } from "./api";
import { ConnectionStore } from "./api/connectionStore";
import { StartupConfigurationError } from "./api/errors";
import type { JsonValue } from "./api/json";
import type { RpcEventHandler, RpcSubscription, RpcTransport } from "./api/transport";
import { createGuiLogger } from "./app/logging";
import type { AppServices } from "./app/services";

const defaultServerEndpoint = "ws://127.0.0.1:53082/rpc";
const browserRpcEndpointSearchParam = "appRpcEndpoint";
const browserRpcEndpointEnvVar = "VITE_APP_RPC_ENDPOINT";
const platformAttribute = "data-platform";
const themeAttribute = "data-theme";
const nativeDialogThemeSearchParam = "__appTheme";
let productionContextMenuGuardInstalled = false;
const stringValueSchema = z.string();

export async function createDefaultAppServices(): Promise<AppServices> {
  installProductionContextMenuGuard(import.meta.env.PROD);
  applyNativeDialogThemeOverride();
  const bootstrapNativeBridge = createAutoNativeBridge("unknown");
  const platform = await bootstrapNativeBridge.app.resolvePlatform().catch(() => "unknown" as const);
  applyNativePlatform(platform);
  const nativeBridge = createAutoNativeBridge(platform);
  const logger = createGuiLogger(nativeBridge);
  const context = await nativeBridge.app
    .resolveContext()
    .catch((error: unknown) => new StartupConfigurationError(errorMessage(error)));
  if (context instanceof Error) {
    await logger.append("error", "Native context resolution failed.", {
      error: context.message,
    });
    return {
      api: new ApiClient(new BootstrapErrorTransport(context)),
      debugThemeOverrideEnabled: import.meta.env.DEV,
      endpoint: defaultServerEndpoint,
      homePath: "",
      logger,
      nativeBridge,
    };
  }
  applyConfiguredTheme(context.theme);
  applyNativeDialogThemeOverride();
  const browserEndpoint = nativeBridge.capabilities.platform === "browser" ? readBrowserRpcEndpoint() : null;
  if (browserEndpoint instanceof Error) {
    await logger.append("error", "Browser RPC endpoint configuration failed.", {
      error: browserEndpoint.message,
    });
    return {
      api: new ApiClient(new BootstrapErrorTransport(browserEndpoint)),
      debugThemeOverrideEnabled: import.meta.env.DEV,
      endpoint: defaultServerEndpoint,
      homePath: context.homePath,
      logger,
      nativeBridge,
    };
  }
  const endpoint =
    browserEndpoint ?? (context.serverEndpoint.length > 0 ? context.serverEndpoint : defaultServerEndpoint);
  // Browser QA points at an arbitrary endpoint, so root validation only applies
  // to the native-resolved server. context.persistenceRootId is empty for the
  // default root (validation skipped).
  const expectedRootId = browserEndpoint === null ? context.persistenceRootId : "";
  const api = new ApiClient(createJsonRpcTransport(endpoint, expectedRootId));
  return {
    api,
    debugThemeOverrideEnabled: import.meta.env.DEV,
    endpoint,
    homePath: context.homePath,
    logger,
    nativeBridge,
  };
}

export function applyNativePlatform(platform: NativePlatform): void {
  if (typeof document === "undefined") {
    return;
  }
  document.documentElement.setAttribute(platformAttribute, platform);
}

export function readBrowserRpcEndpoint(): string | null | StartupConfigurationError {
  const raw = firstNonEmptyString(
    readBrowserRpcEndpointSearchParam(),
    import.meta.env[browserRpcEndpointEnvVar],
  );
  if (raw === null) {
    return defaultServerEndpoint;
  }
  const endpoint = parseBrowserRpcEndpoint(raw);
  if (endpoint instanceof Error) {
    return endpoint;
  }
  return endpoint;
}

export function parseBrowserRpcEndpoint(raw: string): string | StartupConfigurationError {
  const trimmed = raw.trim();
  if (trimmed.length === 0) {
    return new StartupConfigurationError("Browser RPC endpoint is empty.");
  }
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return new StartupConfigurationError("Browser RPC endpoint must be an absolute ws:// or wss:// URL.");
  }
  if (parsed.protocol !== "ws:" && parsed.protocol !== "wss:") {
    return new StartupConfigurationError("Browser RPC endpoint must use ws:// or wss://.");
  }
  if (parsed.username.length > 0 || parsed.password.length > 0) {
    return new StartupConfigurationError("Browser RPC endpoint must not include credentials.");
  }
  if (parsed.hash.length > 0) {
    return new StartupConfigurationError("Browser RPC endpoint must not include a URL fragment.");
  }
  return parsed.toString();
}

export function applyConfiguredTheme(theme: string): void {
  if (typeof document === "undefined") {
    return;
  }
  if (theme === "light" || theme === "dark") {
    document.documentElement.setAttribute(themeAttribute, theme);
    return;
  }
  document.documentElement.removeAttribute(themeAttribute);
}

export function applyNativeDialogThemeOverride(): void {
  const theme = readNativeDialogThemeOverride();
  if (theme !== null) {
    setInMemoryThemeOverride(theme);
  }
}

export type AppTheme = "light" | "dark";

export function readEffectiveTheme(): AppTheme {
  if (typeof document !== "undefined") {
    const configured = document.documentElement.getAttribute(themeAttribute);
    if (configured === "light" || configured === "dark") {
      return configured;
    }
  }
  if (typeof window === "undefined" || !(window.matchMedia instanceof Function)) {
    return "dark";
  }
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

export function setInMemoryThemeOverride(theme: AppTheme): void {
  if (typeof document === "undefined") {
    return;
  }
  document.documentElement.setAttribute(themeAttribute, theme);
}

function readBrowserRpcEndpointSearchParam(): string | null {
  if (typeof window === "undefined") {
    return null;
  }
  return new URLSearchParams(window.location.search).get(browserRpcEndpointSearchParam);
}

function firstNonEmptyString(...values: readonly unknown[]): string | null {
  for (const value of values) {
    const parsed = stringValueSchema.safeParse(value);
    if (parsed.success && parsed.data.trim().length > 0) {
      return parsed.data;
    }
  }
  return null;
}

function readNativeDialogThemeOverride(): AppTheme | null {
  if (typeof window === "undefined" || !window.location.pathname.startsWith("/native-dialog/")) {
    return null;
  }
  const theme = new URLSearchParams(window.location.search).get(nativeDialogThemeSearchParam);
  return theme === "light" || theme === "dark" ? theme : null;
}

export function toggleInMemoryThemeOverride(): AppTheme {
  const nextTheme = readEffectiveTheme() === "dark" ? "light" : "dark";
  setInMemoryThemeOverride(nextTheme);
  return nextTheme;
}

export function installProductionContextMenuGuard(isProduction: boolean): void {
  if (!isProduction || productionContextMenuGuardInstalled || typeof document === "undefined") {
    return;
  }
  productionContextMenuGuardInstalled = true;
  document.addEventListener(
    "contextmenu",
    (event) => {
      event.preventDefault();
    },
  );
}

class BootstrapErrorTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  readonly #error: Error;

  constructor(error: Error) {
    this.#error = error;
    this.connection.set("disconnected", error.message);
  }

  async call(): Promise<unknown> {
    throw this.#error;
  }

  subscribe(_method: string, _params: JsonValue, handler: RpcEventHandler): RpcSubscription {
    handler.onError(this.#error);
    return {
      close() {
        return;
      },
    };
  }
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  const parsed = stringValueSchema.safeParse(error);
  if (parsed.success) {
    return parsed.data;
  }
  return "Unknown native context error.";
}

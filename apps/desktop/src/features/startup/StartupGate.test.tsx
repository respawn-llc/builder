import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createBrowserNativeBridge } from "@app/native-bridge";

import { App } from "../../App";
import { ServerRootMismatchError, StartupConfigurationError, TransportError } from "../../api/errors";
import { protocolVersion } from "../../api/jsonRpcSocket";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";
import { serverSetupDocsUrl } from "../../appLinks";

describe("StartupGate", () => {
  it("guides server setup instead of erroring when the server is unreachable", async () => {
    render(
      <App
        services={createTestServices([
          { method: "server.readiness.get", result: {}, error: new TransportError("connection refused") },
        ])}
      />,
    );

    expect(await screen.findByTestId("server-setup-guide", undefined, { timeout: 4_000 })).toBeInTheDocument();
    expect(screen.queryByTestId("error-state")).not.toBeInTheDocument();
    expect(screen.queryByTestId("home-route-root")).not.toBeInTheDocument();
  });

  it("rechecks readiness when the setup guide check-again button is used", async () => {
    const services = createTestServices([
      { method: "server.readiness.get", result: {}, error: new TransportError("connection refused") },
    ]);
    render(<App services={services} />);

    await screen.findByTestId("server-setup-guide", undefined, { timeout: 4_000 });
    const before = services.transport.calls.filter((call) => call.method === "server.readiness.get").length;

    fireEvent.click(screen.getByTestId("server-setup-check-again"));

    await waitFor(() => {
      expect(services.transport.calls.filter((call) => call.method === "server.readiness.get").length).toBeGreaterThan(
        before,
      );
    });
  });

  it("opens the server setup documentation from the guide", async () => {
    const opened: string[] = [];
    const bridge = {
      ...createBrowserNativeBridge(),
      links: {
        async openExternal(url: string): Promise<void> {
          opened.push(url);
        },
      },
    };
    render(
      <App
        services={createTestServices(
          [{ method: "server.readiness.get", result: {}, error: new TransportError("connection refused") }],
          bridge,
        )}
      />,
    );

    await screen.findByTestId("server-setup-guide", undefined, { timeout: 4_000 });
    fireEvent.click(screen.getByTestId("server-setup-open-docs"));

    expect(opened).toEqual([serverSetupDocsUrl]);
  });

  it("surfaces not-ready auth and startup causes", async () => {
    render(
      <App
        services={createTestServices([
          {
            method: "server.readiness.get",
            result: {
              ready: false,
              server_id: "server-1",
              server_version: "1.3.0",
              protocol_version: protocolVersion,
              auth_ready: false,
              auth_required: true,
              endpoint: "ws://127.0.0.1:53082/rpc",
              causes: [
                {
                  code: "auth",
                  severity: "error",
                  summary: "Auth required",
                  next_action: "Run kent auth login",
                  diagnostic_id: "diag-1",
                },
              ],
            },
          },
        ])}
      />,
    );

    expect(await screen.findByTestId("error-state")).toBeInTheDocument();
  });

  it("renders localized readiness fallback for typed causes without server copy", async () => {
    render(
      <App
        services={createTestServices([
          {
            method: "server.readiness.get",
            result: {
              ready: false,
              server_id: "server-1",
              server_version: "1.3.0",
              protocol_version: protocolVersion,
              auth_ready: false,
              auth_required: true,
              endpoint: "ws://127.0.0.1:53082/rpc",
              causes: [
                {
                  code: "onboarding_required",
                  severity: "error",
                  diagnostic_id: "onboarding_required",
                },
              ],
            },
          },
        ])}
      />,
    );

    expect(await screen.findByText("Readiness check failed.")).toBeInTheDocument();
  });

  it("surfaces native configuration errors without service-install guidance", async () => {
    render(
      <App
        services={createTestServices([
          {
            method: "server.readiness.get",
            result: {},
            error: new StartupConfigurationError("invalid Kent config"),
          },
        ])}
      />,
    );

    expect(await screen.findByTestId("error-state", undefined, { timeout: 4_000 })).toBeInTheDocument();
    expect(screen.queryByTestId("server-setup-guide")).not.toBeInTheDocument();
  });

  it("surfaces a server root mismatch as a startup error, not the setup guide", async () => {
    render(
      <App
        services={createTestServices([
          {
            method: "server.readiness.get",
            result: {},
            error: new ServerRootMismatchError("server serves a different persistence root"),
          },
        ])}
      />,
    );

    expect(await screen.findByTestId("error-state", undefined, { timeout: 4_000 })).toBeInTheDocument();
    expect(screen.queryByTestId("server-setup-guide")).not.toBeInTheDocument();
  });

  it("does not call removed backend capabilities route before showing app content", async () => {
    const services = createTestServices(startupRoutes);

    render(
      <App services={services} />,
    );

    expect(await screen.findByTestId("home-route-root")).toBeInTheDocument();
    expect(services.transport.calls.map((call) => call.method)).not.toContain("server.capabilities.get");
  });

  it("refreshes readiness after reconnecting from a disconnected status", async () => {
    const services = createTestServices(startupRoutes);

    render(<App services={services} />);

    expect(await screen.findByTestId("home-route-root")).toBeInTheDocument();
    const readinessCallsBeforeReconnect = services.transport.calls.filter(
      (call) => call.method === "server.readiness.get",
    ).length;

    act(() => {
      services.transport.connection.set("disconnected", "closed");
    });

    act(() => {
      services.transport.connection.set("connected");
    });

    await waitFor(() => {
      expect(services.transport.calls.filter((call) => call.method === "server.readiness.get").length).toBe(
        readinessCallsBeforeReconnect + 1,
      );
    });
  });

  it("resets to startup when server protocol does not match desktop protocol", async () => {
    window.history.pushState(null, "", "/workflows/workflow-1/editor");
    render(
      <App
        services={createTestServices([
          {
            method: "server.readiness.get",
            result: {
              ready: true,
              server_id: "server-1",
              server_version: "1.3.0",
              protocol_version: "1",
              auth_ready: true,
              auth_required: true,
              endpoint: "ws://127.0.0.1:53082/rpc",
            },
          },
        ])}
      />,
    );

    expect(await screen.findByTestId("error-state")).toBeInTheDocument();
    expect(
      screen.getByText(
        `Client protocol ${protocolVersion}. Server protocol 1. Update Kent CLI/service and desktop app from the same build.`,
      ),
    ).toBeInTheDocument();
    await waitFor(() => {
      expect(window.location.pathname).toBe("/");
    });
    expect(screen.queryByTestId("home-route-root")).not.toBeInTheDocument();
  });
});

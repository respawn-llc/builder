import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";

import { appI18n } from "@/i18n";
import { useCurrentWindowChromeTitle } from "@/app-facade";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { ChatShell, type SelectedSession } from "./ChatShell";

const selectedSession: SelectedSession = {
  projectID: "project-1",
  sessionID: "session-1",
};

function renderShell(
  state: ComponentProps<typeof ChatShell>["state"],
  sessionName: string | null = "Review chat",
) {
  const services = createTestServices([]);
  let contentSession: SelectedSession | undefined;
  let composerSession: SelectedSession | undefined;
  const view = render(
    <TestAppProviders services={services}>
      <ChatShell
        composer={(session) => {
          composerSession = session;
          return <div data-testid="composer-slot" />;
        }}
        content={(session) => {
          contentSession = session;
          return <div data-testid="content-slot" />;
        }}
        selectedSession={selectedSession}
        sessionName={sessionName}
        state={state}
      />
      <WindowTitleProbe />
    </TestAppProviders>,
  );
  return {
    ...view,
    composerSession: () => composerSession,
    contentSession: () => contentSession,
  };
}

it("passes one selected Session context to both required ready slots", () => {
  const view = renderShell({ kind: "ready" });

  expect(view.contentSession()).toBe(view.composerSession());
  expect(view.contentSession()).toEqual(selectedSession);
  expect(screen.getByTestId("window-title")).toHaveTextContent("Review chat");
  expect(screen.getByTestId("content-slot")).toBeInTheDocument();
  expect(screen.getByTestId("composer-slot")).toBeInTheDocument();
});

it("leaves the global chrome title absent when the Session has no authoritative name", () => {
  renderShell({ kind: "ready" }, null);

  expect(screen.getByTestId("window-title")).toHaveTextContent("");
});

it("suppresses both slots and invokes the supplied Retry operation in the error state", async () => {
  const retry = vi.fn();
  renderShell({ diagnostic: "diagnostic", kind: "error", onRetry: retry });

  expect(screen.queryByTestId("content-slot")).not.toBeInTheDocument();
  expect(screen.queryByTestId("composer-slot")).not.toBeInTheDocument();

  await userEvent.setup().click(screen.getByRole("button", { name: appI18n.t("app.retry") }));
  expect(retry).toHaveBeenCalledOnce();
});

function WindowTitleProbe() {
  const title = useCurrentWindowChromeTitle();
  return <div data-testid="window-title">{title ?? ""}</div>;
}

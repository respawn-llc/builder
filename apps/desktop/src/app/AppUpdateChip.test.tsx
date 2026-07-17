import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { vi } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { AppUpdateChip } from "./AppUpdateChip";
import type { DesktopUpdateState } from "./useDesktopUpdate";

function updateState(overrides: Partial<DesktopUpdateState> = {}): DesktopUpdateState {
  return {
    phase: "available",
    version: "2.2.0",
    progressRatio: null,
    install: vi.fn(),
    dismiss: vi.fn(),
    ...overrides,
  };
}

function renderChip(state: DesktopUpdateState) {
  return render(
    <I18nextProvider i18n={appI18n}>
      <AppUpdateChip state={state} />
    </I18nextProvider>,
  );
}

describe("AppUpdateChip", () => {
  beforeAll(async () => {
    await initializeI18n();
  });

  it("renders nothing when no update is available", () => {
    renderChip(updateState({ phase: "none" }));

    expect(screen.queryByTestId("app-update-chip")).toBeNull();
  });

  it("installs when the pill body is activated", () => {
    const install = vi.fn();
    renderChip(updateState({ phase: "available", install }));

    const installButton = screen.getByTestId("app-update-chip-install");
    expect(installButton).not.toBeDisabled();
    fireEvent.click(installButton);

    expect(install).toHaveBeenCalledOnce();
  });

  it("dismisses when the trailing control is activated", () => {
    const dismiss = vi.fn();
    renderChip(updateState({ phase: "available", dismiss }));

    fireEvent.click(screen.getByTestId("app-update-chip-dismiss"));

    expect(dismiss).toHaveBeenCalledOnce();
  });

  it("disables install and hides dismiss while installing", () => {
    renderChip(updateState({ phase: "installing", progressRatio: 0.5 }));

    expect(screen.getByTestId("app-update-chip-install")).toBeDisabled();
    expect(screen.getByTestId("app-update-chip")).toHaveAttribute("data-phase", "installing");
    expect(screen.queryByTestId("app-update-chip-dismiss")).toBeNull();
  });

  it("stays actionable after a failed install", () => {
    const install = vi.fn();
    renderChip(updateState({ phase: "error", install }));

    const installButton = screen.getByTestId("app-update-chip-install");
    expect(installButton).not.toBeDisabled();
    fireEvent.click(installButton);

    expect(install).toHaveBeenCalledOnce();
  });
});
